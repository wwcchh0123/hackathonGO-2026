package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wwcchh0123/hackathonGO-2026/internal/agentapi"
	"github.com/wwcchh0123/hackathonGO-2026/internal/config"
	"github.com/wwcchh0123/hackathonGO-2026/internal/controlplane"
	"github.com/wwcchh0123/hackathonGO-2026/internal/session"
)

type adminDeviceTransport struct {
	device, conversation, token string
}

func (t adminDeviceTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+t.token)
	if r.Method == http.MethodPost {
		r.Header.Set("X-Linx-Device-Id", t.device)
		r.Header.Set("X-Linx-Session-Id", t.conversation)
	}
	return http.DefaultTransport.RoundTrip(r)
}

func adminCallClient(t *testing.T, ctx context.Context, endpoint, device string) (*mcp.ClientSession, func()) {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "admin-e2e", Version: "1"}, nil)
	hc := &http.Client{Transport: adminDeviceTransport{device: device, conversation: "conversation-" + device, token: "control-secret"}}
	cloud, err := client.Connect(ctx, &mcp.SSEClientTransport{Endpoint: endpoint + "/mcp", HTTPClient: hc}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return cloud, func() { _ = cloud.Close() }
}

func adminJSON(t *testing.T, client *http.Client, ctx context.Context, method, url string, want int, out any) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		t.Fatalf("%s: got %d want %d: %s", url, resp.StatusCode, want, raw)
	}
	if out != nil && json.Unmarshal(raw, out) != nil {
		t.Fatalf("invalid JSON from %s: %s", url, raw)
	}
}

func waitIdleTask(t *testing.T, c *session.Controller, receipt session.Receipt) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		zero := 0
		detail, err := c.GetTask(context.Background(), receipt.TaskRef, session.DetailOptions{CommandID: receipt.CommandID, EventsLimit: &zero})
		if err != nil {
			t.Fatal(err)
		}
		if detail.Status == "idle" {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("task did not become idle")
}

func TestAdminCenterMultiDeviceIsolationAndConcurrentTasks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	mock := agentapi.NewMock(ctx)
	mock.Step = 2 * time.Millisecond
	manager := controlplane.New(ctx, mock, "device-a", t.TempDir(), "http://example.test", controlplane.Options{OnlineWindow: 200 * time.Millisecond})
	h := HandlerWithManager(config.Config{Mode: "mock", DeviceID: "device-a", Token: "control-secret", DataDir: t.TempDir(), DevTools: true}, manager)
	ts := httptest.NewServer(h)
	defer ts.Close()
	defer func() { cancel(); manager.Wait() }()

	a, closeA := adminCallClient(t, ctx, ts.URL, "device-a")
	defer closeA()
	b, closeB := adminCallClient(t, ctx, ts.URL, "device-b")
	defer closeB()

	newTask := func(client *mcp.ClientSession, text string) session.Receipt {
		t.Helper()
		result, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "new_task", Arguments: map[string]any{"text": text}})
		if err != nil {
			t.Fatal(err)
		}
		return receiptResult(t, result)
	}
	var ra, rb session.Receipt
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); ra = newTask(a, "设备 A 的任务") }()
	go func() { defer wg.Done(); rb = newTask(b, "设备 B 的任务") }()
	wg.Wait()
	if ra.TaskRef == rb.TaskRef {
		t.Fatal("device task references collided")
	}
	waitDetail(t, manager.DefaultController(), ra)
	bc, ok := manager.Controller("device-b")
	if !ok {
		t.Fatal("device b was not registered")
	}
	waitDetail(t, bc, rb)
	ra2 := newTask(a, "设备 A 的第二个任务")
	waitDetail(t, manager.DefaultController(), ra2)
	waitIdleTask(t, manager.DefaultController(), ra)
	waitIdleTask(t, manager.DefaultController(), ra2)
	waitIdleTask(t, bc, rb)
	streamClient, err := mcp.NewClient(&mcp.Implementation{Name: "admin-streamable", Version: "1"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp", HTTPClient: &http.Client{Transport: adminDeviceTransport{device: "device-c", conversation: "conversation-device-c", token: "control-secret"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer streamClient.Close()
	rc := newTask(streamClient, "设备 C 的任务")
	cc, ok := manager.Controller("device-c")
	if !ok {
		t.Fatal("streamable device was not registered")
	}
	waitDetail(t, cc, rc)
	waitIdleTask(t, cc, rc)

	hc := &http.Client{Transport: adminDeviceTransport{token: "control-secret"}}
	var list struct {
		Data []controlplane.DeviceOverview `json:"data"`
	}
	adminJSON(t, hc, ctx, http.MethodGet, ts.URL+"/api/admin/devices", http.StatusOK, &list)
	if len(list.Data) != 3 {
		t.Fatalf("device list: %+v", list.Data)
	}
	byID := map[string]controlplane.DeviceOverview{}
	for _, d := range list.Data {
		byID[d.DeviceID] = d
	}
	if byID["device-a"].TaskCounts.Total != 2 || byID["device-b"].TaskCounts.Total != 1 || byID["device-c"].TaskCounts.Total != 1 {
		t.Fatalf("task counts leaked across devices: %+v", byID)
	}
	if !byID["device-a"].Online || !byID["device-b"].Online {
		t.Fatalf("recent MCP touch not reported online: %+v", byID)
	}

	var aTasks struct {
		DeviceID string           `json:"device_id"`
		Tasks    session.TaskPage `json:"tasks"`
	}
	adminJSON(t, hc, ctx, http.MethodGet, ts.URL+"/api/admin/devices/device-a/tasks?limit=50", http.StatusOK, &aTasks)
	if aTasks.DeviceID != "device-a" || len(aTasks.Tasks.Data) != 2 {
		t.Fatalf("device a tasks: %+v", aTasks)
	}
	var global struct {
		Data    []controlplane.AdminTask `json:"data"`
		HasMore bool                     `json:"has_more"`
	}
	adminJSON(t, hc, ctx, http.MethodGet, ts.URL+"/api/admin/tasks?query="+url.QueryEscape("设备 A")+"&limit=1", http.StatusOK, &global)
	if len(global.Data) != 1 || global.Data[0].DeviceID != "device-a" || !global.HasMore {
		t.Fatalf("global task search/pagination: %+v", global)
	}
	var dashboard controlplane.Dashboard
	adminJSON(t, hc, ctx, http.MethodGet, ts.URL+"/api/admin/overview", http.StatusOK, &dashboard)
	if dashboard.DeviceCount != 3 || dashboard.RunningTasks != 0 {
		t.Fatalf("dashboard aggregation: %+v", dashboard)
	}
	var searchedDashboard controlplane.Dashboard
	adminJSON(t, hc, ctx, http.MethodGet, ts.URL+"/api/admin/overview?query="+url.QueryEscape("设备 A"), http.StatusOK, &searchedDashboard)
	if len(searchedDashboard.Tasks) != 2 || len(searchedDashboard.Devices) != 1 || searchedDashboard.Devices[0].DeviceID != "device-a" {
		t.Fatalf("dashboard search: %+v", searchedDashboard)
	}
	var deviceDetail struct {
		Device controlplane.DeviceOverview `json:"device"`
	}
	adminJSON(t, hc, ctx, http.MethodGet, ts.URL+"/api/admin/devices/device-a", http.StatusOK, &deviceDetail)
	if deviceDetail.Device.DeviceID != "device-a" {
		t.Fatalf("device detail: %+v", deviceDetail)
	}
	var globalDetail struct {
		DeviceID string         `json:"device_id"`
		Task     session.Detail `json:"task"`
	}
	adminJSON(t, hc, ctx, http.MethodGet, ts.URL+"/api/admin/tasks/device-a/"+ra.TaskRef+"?events_limit=5", http.StatusOK, &globalDetail)
	if globalDetail.DeviceID != "device-a" || globalDetail.Task.Ref != ra.TaskRef {
		t.Fatalf("global task detail: %+v", globalDetail)
	}
	adminJSON(t, hc, ctx, http.MethodGet, ts.URL+"/api/admin/tasks/device-b/"+ra.TaskRef, http.StatusNotFound, nil)
	adminJSON(t, hc, ctx, http.MethodGet, ts.URL+"/api/admin/devices?health=bad", http.StatusBadRequest, nil)
	adminJSON(t, hc, ctx, http.MethodGet, ts.URL+"/api/admin/tasks?status=bad", http.StatusBadRequest, nil)
	var limited struct {
		Tasks session.TaskPage `json:"tasks"`
	}
	adminJSON(t, hc, ctx, http.MethodGet, ts.URL+"/api/admin/devices/device-a/tasks?limit=1&status=idle", http.StatusOK, &limited)
	if len(limited.Tasks.Data) != 1 || !limited.Tasks.HasMore {
		t.Fatalf("device task pagination/filter: %+v", limited.Tasks)
	}
	var detail struct {
		DeviceID string         `json:"device_id"`
		Task     session.Detail `json:"task"`
	}
	adminJSON(t, hc, ctx, http.MethodGet, ts.URL+"/api/admin/devices/device-a/tasks/"+ra.TaskRef+"?events_limit=10", http.StatusOK, &detail)
	if detail.DeviceID != "device-a" || detail.Task.Ref != ra.TaskRef || len(detail.Task.Events) == 0 {
		t.Fatalf("device a detail missing evidence: %+v", detail)
	}
	adminJSON(t, hc, ctx, http.MethodGet, ts.URL+"/api/admin/devices/device-b/tasks/"+ra.TaskRef, http.StatusNotFound, nil)

	// Management reads never alter the device's selected display target.
	if manager.DefaultController().State.Get().TaskRef != "" || bc.State.Get().TaskRef != "" {
		t.Fatal("admin query changed display selection")
	}
	time.Sleep(250 * time.Millisecond)
	adminJSON(t, hc, ctx, http.MethodGet, ts.URL+"/api/admin/devices", http.StatusOK, &list)
	for _, d := range list.Data {
		if d.Online {
			t.Fatalf("device did not become stale: %+v", d)
		}
	}

	unauth := httptest.NewRecorder()
	h.ServeHTTP(unauth, httptest.NewRequest(http.MethodGet, "/api/admin/devices", nil))
	if unauth.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated admin request: %d", unauth.Code)
	}
	page := httptest.NewRecorder()
	h.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/admin", nil))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Control Center") {
		t.Fatalf("admin page: %d %s", page.Code, page.Body.String())
	}
}

type failingSessionsProvider struct {
	agentapi.Provider
	fail bool
}

func (p *failingSessionsProvider) ListSessions(ctx context.Context, q agentapi.SessionQuery) (agentapi.SessionPage, error) {
	if p.fail {
		return agentapi.SessionPage{}, &agentapi.Error{Operation: "GET sessions", StatusCode: http.StatusServiceUnavailable}
	}
	return p.Provider.ListSessions(ctx, q)
}

func TestAdminCenterKeepsDeviceVisibleWhenTaskQueryFails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider := &failingSessionsProvider{Provider: agentapi.NewMock(ctx), fail: true}
	manager := controlplane.New(ctx, provider, "device-a", t.TempDir(), "http://example.test")
	defer func() { cancel(); manager.Wait() }()
	if err := manager.Touch("device-b", "test"); err != nil {
		t.Fatal(err)
	}
	data, _ := manager.ListDevices(ctx)
	if len(data) != 2 {
		t.Fatal(data)
	}
	for _, d := range data {
		if d.TasksError == "" {
			t.Fatalf("missing partial task error: %+v", d)
		}
	}
}
