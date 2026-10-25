package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wwcchh0123/hackathonGO-2026/internal/agentapi"
	"github.com/wwcchh0123/hackathonGO-2026/internal/config"
	"github.com/wwcchh0123/hackathonGO-2026/internal/controlplane"
	"github.com/wwcchh0123/hackathonGO-2026/internal/runtimeprofile"
	"github.com/wwcchh0123/hackathonGO-2026/internal/session"
)

func TestRuntimeProfilesMCPHTTPAndDisplayContracts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var bindings []session.ProfileBinding
	for _, p := range runtimeprofile.MockRegistry().Profiles {
		m := agentapi.NewMockForProfile(ctx, p)
		m.Step = time.Millisecond
		bindings = append(bindings, session.ProfileBinding{Profile: p, Provider: m})
	}
	ts := httptest.NewUnstartedServer(nil)
	base := "http://" + ts.Listener.Addr().String()
	dir := t.TempDir()
	manager := controlplane.New(ctx, bindings[0].Provider, "test-device", dir, base, controlplane.Options{Session: session.Options{Profiles: bindings, DefaultProfile: "claudecode"}})
	defer func() { cancel(); manager.Wait() }()
	ts.Config.Handler = HandlerWithManager(config.Config{Mode: "mock", DeviceID: "test-device", Token: "test-control-token", PublicURL: base, DataDir: dir, DevTools: true}, manager)
	ts.Start()
	defer ts.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "runtime-profile-test", Version: "1"}, nil)
	conn, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: base + "/mcp", HTTPClient: &http.Client{Transport: authTransport{}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	call := func(name string, args any) *mcp.CallToolResult {
		t.Helper()
		res, err := conn.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	receipts := map[string]session.Receipt{}
	for _, runtime := range []string{"claudecode", "codex", "native"} {
		res := call("new_task", map[string]any{"text": "counter", "runtime": runtime, "model": map[string]string{"id": "mock/" + runtime}})
		r := receiptResult(t, res)
		d := waitDetail(t, manager.DefaultController(), r)
		if d.Runtime != runtime || r.Runtime != runtime {
			t.Fatal(r, d)
		}
		receipts[runtime] = r
	}
	r := receipts["codex"]
	reject := call("send_message", map[string]any{"text": "x", "runtime": "native", "task_ref": r.TaskRef})
	var envelope map[string]any
	_ = json.Unmarshal([]byte(voiceResult(t, reject, true)), &envelope)
	if envelope["code"] != "runtime_switch_required" || envelope["current"] != "codex" || envelope["requested"] != "native" {
		t.Fatal(envelope)
	}
	raw := voiceResult(t, call("list_tasks", map[string]any{"runtime": "codex", "model": map[string]string{"id": "mock/codex"}}), false)
	var page session.TaskPage
	_ = json.Unmarshal([]byte(raw), &page)
	if len(page.Data) != 1 || page.Partial || !page.Complete {
		t.Fatal(page)
	}
	httpClient := &http.Client{Transport: authTransport{}}
	resp, err := httpClient.Get(base + "/api/tasks?runtime=codex&model=mock%2Fcodex")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(data), `"runtime":"codex"`) {
		t.Fatal(string(data))
	}
	req, _ := http.NewRequest("POST", base+"/api/commands", strings.NewReader(`{"action":"message","task_ref":"`+r.TaskRef+`","prompt":"x","model":{"id":"other"}}`))
	resp, err = httpClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 400 || !strings.Contains(string(data), `"code":"model_switch_required"`) {
		t.Fatal(string(data))
	}
	resp, err = http.Get(base + "/display/sessions")
	if err != nil {
		t.Fatal(err)
	}
	data, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(data), `"model":{"id":"mock/codex"}`) || !strings.Contains(string(data), `"complete":true`) {
		t.Fatal(string(data))
	}
}

func TestRuntimeCatalogPaginationFitsMCPRequestLimit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var bindings []session.ProfileBinding
	random := rand.New(rand.NewPCG(42, 54))
	for _, profile := range runtimeprofile.MockRegistry().Profiles {
		provider := agentapi.NewMockForProfile(ctx, profile)
		for range 50 {
			title := make([]rune, 100)
			for j := range title {
				title[j] = rune(0x4e00 + random.IntN(0x4fff))
			}
			if _, err := provider.Create(ctx, agentapi.NewSession{Title: string(title)}); err != nil {
				t.Fatal(err)
			}
		}
		bindings = append(bindings, session.ProfileBinding{Profile: profile, Provider: provider})
	}
	ts := httptest.NewUnstartedServer(nil)
	base := "http://" + ts.Listener.Addr().String()
	dir := t.TempDir()
	manager := controlplane.New(ctx, bindings[0].Provider, "test-device", dir, base, controlplane.Options{Session: session.Options{Profiles: bindings, DefaultProfile: "claudecode"}})
	defer func() { cancel(); manager.Wait() }()
	handler := HandlerWithManager(config.Config{Mode: "mock", DeviceID: "test-device", Token: "test-control-token", PublicURL: base, DataDir: dir}, manager)
	var largestRequest atomic.Int64
	ts.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for previous := largestRequest.Load(); r.ContentLength > previous; previous = largestRequest.Load() {
			if largestRequest.CompareAndSwap(previous, r.ContentLength) {
				break
			}
		}
		handler.ServeHTTP(w, r)
	})
	ts.Start()
	defer ts.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "pagination-wire-test", Version: "1"}, nil)
	conn, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: base + "/mcp", HTTPClient: &http.Client{Transport: authTransport{}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	seen := map[string]bool{}
	token := ""
	for pages := 0; pages < 10; pages++ {
		result, err := conn.CallTool(ctx, &mcp.CallToolParams{Name: "list_tasks", Arguments: map[string]any{"limit": 20, "page": token}})
		if err != nil {
			t.Fatal(err)
		}
		var page session.TaskPage
		if err := json.Unmarshal([]byte(voiceResult(t, result, false)), &page); err != nil {
			t.Fatal(err)
		}
		if page.Partial || !page.Complete {
			t.Fatal(page)
		}
		for _, task := range page.Data {
			if seen[task.Ref] {
				t.Fatal("duplicate task", task.Ref)
			}
			seen[task.Ref] = true
		}
		if !page.HasMore {
			break
		}
		token = page.NextPage
	}
	if len(seen) != 150 {
		t.Fatalf("MCP pagination lost tasks: %d", len(seen))
	}
	if largestRequest.Load() <= 0 || largestRequest.Load() > 4096 {
		t.Fatalf("pagination request exceeds wire budget: %d", largestRequest.Load())
	}
}
