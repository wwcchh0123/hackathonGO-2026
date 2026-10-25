package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wwcchh0123/hackathonGO-2026/internal/agentapi"
	"github.com/wwcchh0123/hackathonGO-2026/internal/artifacts"
	"github.com/wwcchh0123/hackathonGO-2026/internal/config"
	"github.com/wwcchh0123/hackathonGO-2026/internal/device"
	"github.com/wwcchh0123/hackathonGO-2026/internal/events"
	"github.com/wwcchh0123/hackathonGO-2026/internal/session"
)

type authTransport struct{}

func (authTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer test-control-token")
	r.Header.Set("X-Linx-Device-Id", "test-device")
	return http.DefaultTransport.RoundTrip(r)
}

type linxTransport struct {
	mu           sync.Mutex
	conversation string
}

func (a *linxTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer test-control-token")
	if r.Method == "POST" {
		r.Header.Set("X-Linx-Device-Id", "test-device")
		a.mu.Lock()
		r.Header.Set("X-Linx-Session-Id", a.conversation)
		a.mu.Unlock()
	}
	return http.DefaultTransport.RoundTrip(r)
}
func (a *linxTransport) setConversation(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.conversation = id
}
func voiceResult(t *testing.T, res *mcp.CallToolResult, failed bool) string {
	t.Helper()
	if res == nil || res.IsError != failed || len(res.Content) != 1 || res.StructuredContent != nil {
		t.Fatalf("invalid result %+v", res)
	}
	value, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatal(res)
	}
	return value.Text
}
func receiptResult(t *testing.T, res *mcp.CallToolResult) session.Receipt {
	t.Helper()
	var r session.Receipt
	if err := json.Unmarshal([]byte(voiceResult(t, res, false)), &r); err != nil || !r.Accepted || r.TaskRef == "" || r.CommandID == "" {
		t.Fatal(r, err)
	}
	return r
}
func waitDetail(t *testing.T, c *session.Controller, r session.Receipt) session.Detail {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		zero := 0
		d, err := c.GetTask(context.Background(), r.TaskRef, session.DetailOptions{CommandID: r.CommandID, EventsLimit: &zero})
		if err != nil {
			t.Fatal(err)
		}
		if d.Command.State == "confirmed" {
			return d
		}
		if d.Command.State == "failed" {
			t.Fatal(d.Command)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("command not confirmed")
	return session.Detail{}
}
func TestMCPToIndependentDeviceStreamAndAutomaticPreview(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	mock := agentapi.NewMock(ctx)
	mock.Step = 5 * time.Millisecond
	ts := httptest.NewUnstartedServer(nil)
	base := "http://" + ts.Listener.Addr().String()
	cfg := config.Config{Mode: "mock", DeviceID: "test-device", Token: "test-control-token", DataDir: t.TempDir(), PublicURL: base, DevTools: true}
	c := session.New(ctx, mock, device.NewState(cfg.DeviceID), &artifacts.Store{Root: cfg.DataDir, PublicURL: base})
	ts.Config.Handler = Handler(cfg, c)
	ts.Start()
	defer ts.Close()
	defer func() { cancel(); c.Wait() }()
	client := &http.Client{Transport: authTransport{}}
	request := func(method, path, body string, want int) []byte {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, method, base+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("%s: %d %s", path, resp.StatusCode, raw)
		}
		return raw
	}
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(base, "http")+"/api/device/channel", &websocket.DialOptions{HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	if _, raw, err := ws.Read(ctx); err != nil || !strings.Contains(string(raw), "test-device") {
		t.Fatal(string(raw), err)
	}
	cloud, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, &mcp.SSEClientTransport{Endpoint: base + "/mcp", HTTPClient: client}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cloud.Close()
	tools, err := cloud.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 4 {
		t.Fatal(tools, err)
	}
	result, err := cloud.CallTool(ctx, &mcp.CallToolParams{Name: "new_task", Arguments: map[string]any{"text": "番茄钟"}})
	if err != nil {
		t.Fatal(err)
	}
	r := receiptResult(t, result)
	d := waitDetail(t, c, r)
	if c.State.Get().TaskRef != "" {
		t.Fatal("MCP write silently selected display")
	}
	request("GET", "/api/tasks?limit=1", "", 200)
	request("GET", "/api/tasks/"+r.TaskRef+"?events_limit=0&command_id="+r.CommandID, "", 200)
	request("POST", "/api/device/view", `{"task_ref":"`+r.TaskRef+`"}`, 200)
	req, _ := http.NewRequestWithContext(ctx, "GET", base+"/api/tasks/"+r.TaskRef+"/events", nil)
	stream, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	stop := errors.New("done")
	count := 0
	err = agentapi.DecodeSSE(stream.Body, func(f events.Frame) error {
		count++
		if f.Type == "session.status_idle" {
			return stop
		}
		return nil
	})
	stream.Body.Close()
	if !errors.Is(err, stop) || count < 5 {
		t.Fatal(count, err)
	}
	// The public display page can trigger its idle-round preview without the
	// control token. The returned URL is the same cached publication used by
	// the authenticated explicit endpoint below.
	previewRequest, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/display/preview", strings.NewReader(`{"task_ref":"`+r.TaskRef+`"}`))
	previewRequest.Header.Set("Content-Type", "application/json")
	previewResponse, err := http.DefaultClient.Do(previewRequest)
	if err != nil {
		t.Fatal(err)
	}
	previewRaw, _ := io.ReadAll(previewResponse.Body)
	previewResponse.Body.Close()
	if previewResponse.StatusCode != http.StatusOK {
		t.Fatalf("display preview: %d %s", previewResponse.StatusCode, previewRaw)
	}
	var automatic struct {
		Available bool   `json:"available"`
		ResultURL string `json:"result_url"`
	}
	if err := json.Unmarshal(previewRaw, &automatic); err != nil || !automatic.Available || automatic.ResultURL == "" {
		t.Fatalf("automatic preview: %s (%v)", previewRaw, err)
	}
	raw := request("POST", "/api/tasks/"+r.TaskRef+"/preview", "{}", 200)
	var preview map[string]string
	_ = json.Unmarshal(raw, &preview)
	if preview["result_url"] != automatic.ResultURL {
		t.Fatalf("preview was downloaded twice: %q != %q", preview["result_url"], automatic.ResultURL)
	}
	page, err := http.Get(preview["result_url"])
	if err != nil {
		t.Fatal(err)
	}
	content, _ := io.ReadAll(page.Body)
	page.Body.Close()
	if page.StatusCode != 200 || !strings.Contains(string(content), "data-storage-scope") {
		t.Fatal(string(content))
	}
	rawURL, _ := url.Parse(preview["result_url"])
	q := rawURL.Query()
	q.Set("_linxi_raw", "1")
	rawURL.RawQuery = q.Encode()
	rawPage, err := http.Get(rawURL.String())
	if err != nil {
		t.Fatal(err)
	}
	rawContent, _ := io.ReadAll(rawPage.Body)
	rawPage.Body.Close()
	if !strings.Contains(string(rawContent), "番茄钟") || !strings.Contains(rawPage.Header.Get("Content-Security-Policy"), "sandbox allow-scripts allow-forms") {
		t.Fatal("missing isolated interactive artifact", string(rawContent))
	}
	request("GET", "/api/tasks/"+r.TaskRef+"/events?after=evicted", "", 409)
	request("GET", "/api/sessions/wrong/events", "", 404)
	request("GET", "/api/tasks/unknown", "", 404)
	request("GET", "/api/tasks?limit=bad", "", 400)
	request("POST", "/api/device/view", `{"task_ref":"unknown"}`, 404)
	if d.SessionID == "" {
		t.Fatal(d)
	}
}

type gatedProvider struct {
	agentapi.Provider
	gate  chan struct{}
	sends atomic.Int32
}

func (p *gatedProvider) Create(ctx context.Context, input ...agentapi.NewSession) (agentapi.Session, error) {
	select {
	case <-p.gate:
		return p.Provider.Create(ctx, input...)
	case <-ctx.Done():
		return agentapi.Session{}, ctx.Err()
	}
}
func (p *gatedProvider) SendMessage(ctx context.Context, id string, msg agentapi.Message) (agentapi.AcceptedInput, error) {
	p.sends.Add(1)
	return p.Provider.SendMessage(ctx, id, msg)
}
func TestVoiceProtocolIdentityDedupAndFastAdmission(t *testing.T) {
	for _, streamable := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "streamable"}[streamable], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			mock := agentapi.NewMock(ctx)
			mock.Step = time.Hour
			p := &gatedProvider{Provider: mock, gate: make(chan struct{})}
			cfg := config.Config{Mode: "http", Token: "test-control-token", DeviceID: "test-device", DataDir: t.TempDir()}
			c := session.New(ctx, p, device.NewState(cfg.DeviceID), &artifacts.Store{Root: cfg.DataDir})
			ts := httptest.NewServer(Handler(cfg, c))
			defer ts.Close()
			defer func() { cancel(); c.Wait() }()
			headers := &linxTransport{conversation: "conversation-a"}
			client := mcp.NewClient(&mcp.Implementation{Name: "linx", Version: "1"}, nil)
			transport := func() mcp.Transport {
				hc := &http.Client{Transport: headers}
				if streamable {
					return &mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp", HTTPClient: hc}
				}
				return &mcp.SSEClientTransport{Endpoint: ts.URL + "/mcp", HTTPClient: hc}
			}
			cloud, err := client.Connect(ctx, transport(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer cloud.Close()
			tools, err := cloud.ListTools(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			names := []string{}
			for _, tool := range tools.Tools {
				names = append(names, tool.Name)
				if tool.OutputSchema != nil {
					t.Fatal("unverified structured output")
				}
			}
			slices.Sort(names)
			if !slices.Equal(names, []string{"get_task", "list_tasks", "new_task", "send_message"}) {
				t.Fatal(names)
			}
			call := func(name string, args any, failed bool) *mcp.CallToolResult {
				t.Helper()
				started := time.Now()
				res, err := cloud.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args, Meta: mcp.Meta{"linxi.control/request-identity": map[string]string{"conversation": "forged"}}})
				if err != nil {
					t.Fatal(err)
				}
				voiceResult(t, res, failed)
				if (name == "new_task" || name == "send_message") && time.Since(started) >= 300*time.Millisecond {
					t.Fatal("admission blocked")
				}
				return res
			}
			a := receiptResult(t, call("new_task", map[string]any{"text": "A"}, false))
			for range 10 {
				if dup := receiptResult(t, call("new_task", map[string]any{"text": "A"}, false)); dup != a {
					t.Fatal("dedup lost")
				}
			}
			call("get_task", map[string]any{"task_ref": a.TaskRef, "events_limit": 0}, false)
			headers.setConversation("conversation-b")
			call("send_message", map[string]any{"text": "implicit wrong conversation"}, true)
			b := receiptResult(t, call("new_task", map[string]any{"text": "B"}, false))
			ra := receiptResult(t, call("send_message", map[string]any{"text": "same", "task_ref": a.TaskRef}, false))
			rb := receiptResult(t, call("send_message", map[string]any{"text": "same", "task_ref": b.TaskRef}, false))
			if ra.CommandID == rb.CommandID {
				t.Fatal("cross-task dedup")
			}
			call("send_message", map[string]any{"text": "x", "session_id": "forged"}, true)
			call("new_task", map[string]any{"text": " "}, true)
			call("get_task", map[string]any{}, true)
			call("list_tasks", map[string]any{"limit": 1000}, true)
			close(p.gate)
			waitDetail(t, c, rb)
			if p.sends.Load() != 4 {
				t.Fatal(p.sends.Load())
			}
			// Metadata inspection still leaves the selected device view untouched.
			if c.State.Get().TaskRef != "" {
				t.Fatal("query changed display")
			}
		})
	}
}
func TestDeveloperPagesAreOptionalAndEmbedded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	c := session.New(ctx, agentapi.NewMock(ctx), device.NewState("test"), &artifacts.Store{Root: t.TempDir()})
	defer func() { cancel(); c.Wait() }()
	for _, enabled := range []bool{true, false} {
		h := Handler(config.Config{Mode: "mock", DeviceID: "test", DevTools: enabled, DataDir: t.TempDir()}, c)
		for _, path := range []string{"/simulator", "/device", "/dev-assets/app.js", "/dev-assets/style.css"} {
			r := httptest.NewRecorder()
			h.ServeHTTP(r, httptest.NewRequest("GET", path, nil))
			want := 404
			if enabled {
				want = 200
			}
			if r.Code != want {
				t.Errorf("DEV_TOOLS=%v %s: got %d want %d", enabled, path, r.Code, want)
			}
		}
	}
}

func TestMCPAuthorizationOnEveryRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	c := session.New(ctx, agentapi.NewMock(ctx), device.NewState("test-device"), &artifacts.Store{Root: t.TempDir()})
	defer func() { cancel(); c.Wait() }()
	for _, mode := range []string{"http", "mock"} {
		ts := httptest.NewServer(Handler(config.Config{Mode: mode, DeviceID: "test-device", Token: "test-control-token", DataDir: t.TempDir()}, c))
		for _, tt := range []struct {
			method, token, device, cookie string
			want                          int
		}{
			{"GET", "", "", "", 401},
			{"GET", "test-control-token", "", "", 401},
			{"GET", "", "", "linxi_control=test-control-token", 401},
			{"GET", "Bearer wrong", "", "", 401},
			{"GET", "Bearer test-control-token", "", "", 200},
			{"GET", "Bearer test-control-token", "other", "", 200},
			{"POST", "", "test-device", "", 401},
			{"POST", "Bearer test-control-token", "wrong", "", 403},
			{"POST", "Bearer test-control-token", "", "", 403},
			{"POST", "Bearer test-control-token", "test-device", "", 404}, // Valid auth, unknown MCP session.
		} {
			path := "/mcp"
			if tt.method == "POST" {
				path = "/mcp/message?sessionid=unknown"
			}
			req, _ := http.NewRequest(tt.method, ts.URL+path, strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", tt.token)
			req.Header.Set("X-Linx-Device-Id", tt.device)
			req.Header.Set("Cookie", tt.cookie)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			want := tt.want
			if mode == "mock" && tt.method == "POST" && tt.token == "Bearer test-control-token" && tt.device == "" {
				want = 404
			}
			if resp.StatusCode != want {
				t.Errorf("%s %+v: status %d want %d", mode, tt, resp.StatusCode, want)
			}
		}
		ts.Close()
	}
}

func TestMCPThroughReverseProxy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	c := session.New(ctx, agentapi.NewMock(ctx), device.NewState("test-device"), &artifacts.Store{Root: t.TempDir()})
	defer func() { cancel(); c.Wait() }()
	upstream := httptest.NewServer(Handler(config.Config{Mode: "http", DeviceID: "test-device", Token: "test-control-token", DataDir: t.TempDir()}, c))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	director := proxy.Director
	proxy.Director = func(r *http.Request) { director(r); r.Host = target.Host } // nginx Host $proxy_host
	// SSE must flush even its first endpoint frame through the proxy.
	proxy.FlushInterval = -1
	entry := httptest.NewServer(proxy)
	defer entry.Close()
	req, _ := http.NewRequestWithContext(ctx, "GET", entry.URL+"/mcp", nil)
	req.Host = "control.example.test"
	req.Header.Set("Authorization", "Bearer test-control-token")
	stream, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	if stream.StatusCode != 200 || stream.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("proxied SSE: %d", stream.StatusCode)
	}
	reader := bufio.NewReader(stream.Body)
	event, _ := reader.ReadString('\n')
	endpoint, _ := reader.ReadString('\n')
	if event != "event: endpoint\n" || !strings.HasPrefix(endpoint, "data: /mcp/message?sessionid=") {
		t.Fatalf("invalid endpoint: %q %q", event, endpoint)
	}
	path := strings.TrimSpace(strings.TrimPrefix(endpoint, "data:"))
	post, _ := http.NewRequestWithContext(ctx, "POST", entry.URL+path, strings.NewReader(`{"jsonrpc":"2.0","method":"initialize","id":1,"params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"proxy-test","version":"1"}}}`))
	post.Host = "control.example.test"
	post.Header.Set("Authorization", "Bearer test-control-token")
	post.Header.Set("X-Linx-Device-Id", "test-device")
	post.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(post)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 202 {
		t.Fatalf("proxied POST: %d", response.StatusCode)
	}
}

func TestPublicDisplayBindsToTaskRefAndReplaysEvents(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mock := agentapi.NewMock(ctx)
	mock.Step = time.Millisecond
	ts := httptest.NewUnstartedServer(nil)
	base := "http://" + ts.Listener.Addr().String()
	cfg := config.Config{Mode: "mock", DeviceID: "test-device", DataDir: t.TempDir(), PublicURL: base}
	c := session.New(ctx, mock, device.NewState(cfg.DeviceID), &artifacts.Store{Root: cfg.DataDir, PublicURL: base})
	ts.Config.Handler = Handler(cfg, c)
	ts.Start()
	defer ts.Close()
	defer func() { c.Wait() }()

	receipt, err := c.Submit(session.Command{Action: "start", Prompt: "展示测试"})
	if err != nil {
		t.Fatal(err)
	}
	waitDetail(t, c, receipt)

	page, err := http.Get(ts.URL + "/display/?task_ref=" + url.QueryEscape(receipt.TaskRef))
	if err != nil {
		t.Fatal(err)
	}
	pageBody, _ := io.ReadAll(page.Body)
	page.Body.Close()
	if page.StatusCode != http.StatusOK || !strings.Contains(string(pageBody), "CodeAgent") {
		t.Fatalf("display page: %d %s", page.StatusCode, pageBody)
	}

	stream, err := http.Get(ts.URL + "/display/events?task_ref=" + url.QueryEscape(receipt.TaskRef))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	if stream.StatusCode != http.StatusOK || stream.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("display stream: %d %q", stream.StatusCode, stream.Header.Get("Content-Type"))
	}
	reader := bufio.NewReader(stream.Body)
	line, err := reader.ReadString('\n')
	if err != nil || line != "event: device-snapshot\n" {
		t.Fatalf("display snapshot: %q (%v)", line, err)
	}
	for range 5 {
		line, err = reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(line, "data:") && strings.Contains(line, `"title":"展示测试"`) {
			return
		}
	}
	t.Fatal("session metadata did not carry the explicit task title")
}

func TestPublicDisplayHeadChecksReferenceWithoutOpeningStreams(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	mock := agentapi.NewMock(ctx)
	mock.Step = time.Millisecond
	c := session.New(ctx, mock, device.NewState("display-head"), &artifacts.Store{Root: t.TempDir()})
	defer func() { cancel(); c.Wait() }()
	r, err := c.Submit(session.Command{Action: "start", Prompt: "head-only fixture"})
	if err != nil {
		t.Fatal(err)
	}
	waitDetail(t, c, r)
	h := Handler(config.Config{Mode: "mock", DeviceID: "display-head", DataDir: t.TempDir(), Token: "private"}, c)
	for range 6 {
		response := httptest.NewRecorder()
		h.ServeHTTP(response, httptest.NewRequest(http.MethodHead, "/display/events?task_ref="+r.TaskRef, nil))
		if response.Code != 200 || response.Body.Len() != 0 || response.Header().Get("Content-Type") == "text/event-stream" {
			t.Fatal(response.Code, response.Body.String())
		}
	}
	if c.State.Get().TaskRef != "" {
		t.Fatal("HEAD selected a display target")
	}
	for _, test := range []struct {
		method, path string
		code         int
	}{
		{http.MethodHead, "/display/events", 400},
		{http.MethodHead, "/display/events?task_ref=missing", 404},
		{http.MethodGet, "/display/events?task_ref=" + r.TaskRef + "&after=missing-cursor", 409},
	} {
		response := httptest.NewRecorder()
		h.ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))
		if response.Code != test.code {
			t.Fatal(test.path, response.Code, response.Body.String())
		}
	}
}

func TestPublicDisplaySessionListIsReadOnlyAndUnprotected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mock := agentapi.NewMock(ctx)
	mock.Step = time.Millisecond
	c := session.New(ctx, mock, device.NewState("test-device"), &artifacts.Store{Root: t.TempDir()})
	defer func() {
		cancel()
		c.Wait()
	}()
	first, err := c.Submit(session.Command{Action: "start", Prompt: "整理发布说明"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.Submit(session.Command{Action: "start", Prompt: "检查移动端布局"})
	if err != nil {
		t.Fatal(err)
	}
	waitDetail(t, c, first)
	waitDetail(t, c, second)

	h := Handler(config.Config{Mode: "http", DeviceID: "test-device", Token: "must-not-be-needed", DataDir: t.TempDir()}, c)
	req := httptest.NewRequest(http.MethodGet, "/display/sessions?limit=10", nil)
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("display session list: %d %s", res.Code, res.Body.String())
	}
	var page displaySessionPage
	if err := json.Unmarshal(res.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Data) != 2 || page.Data[0].TaskRef == "" || page.Data[1].TaskRef == "" {
		t.Fatalf("unexpected display sessions: %+v", page)
	}
	if strings.Contains(res.Body.String(), "must-not-be-needed") || strings.Contains(res.Body.String(), "events") {
		t.Fatalf("display session list leaked protected or event data: %s", res.Body.String())
	}
}
