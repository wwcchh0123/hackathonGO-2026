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
	"unicode/utf8"

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
func TestMCPToDeviceStreamAndPreview(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	provider := agentapi.NewMock(ctx)
	provider.Step = 10 * time.Millisecond
	ts := httptest.NewUnstartedServer(nil)
	base := "http://" + ts.Listener.Addr().String()
	cfg := config.Config{Mode: "mock", DeviceID: "test-device", Token: "test-control-token", DataDir: t.TempDir(), PublicURL: base, DevTools: true}
	c := session.New(ctx, provider, device.NewState(cfg.DeviceID), &artifacts.Store{Root: cfg.DataDir, PublicURL: base})
	ts.Config.Handler = Handler(cfg, c)
	ts.Start()
	defer ts.Close()
	defer func() { cancel(); c.Wait() }()
	unauthorized, err := http.Get(base + "/api/device")
	if err != nil {
		t.Fatal(err)
	}
	unauthorized.Body.Close()
	if unauthorized.StatusCode != 401 {
		t.Fatal("device endpoint was not protected")
	}
	client := &http.Client{Transport: authTransport{}}
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(base, "http")+"/api/device/channel", &websocket.DialOptions{HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	_, initial, err := ws.Read(ctx)
	if err != nil || !strings.Contains(string(initial), "test-device") {
		t.Fatalf("device snapshot: %s %v", initial, err)
	}
	if strings.Contains(string(initial), `"view"`) {
		t.Fatal("cloud snapshot still controls the device view")
	}
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "test-linx", Version: "1"}, nil)
	cloud, err := mcpClient.Connect(ctx, &mcp.SSEClientTransport{Endpoint: base + "/mcp", HTTPClient: client}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cloud.Close()
	tools, err := cloud.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 3 {
		t.Fatalf("tools discovery: %v %v", tools, err)
	}
	res, err := cloud.CallTool(ctx, &mcp.CallToolParams{Name: "send_message", Arguments: map[string]any{"text": "帮我做个番茄钟"}})
	if err != nil || res.IsError {
		t.Fatalf("send_message: %v %v", res, err)
	}
	for c.State.Get().ResultURL == "" {
		select {
		case <-ctx.Done():
			t.Fatalf("no preview: %+v", c.State.Get())
		case <-time.After(time.Millisecond):
		}
	}
	snapshot := c.State.Get()
	_, live, err := ws.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var liveState device.Snapshot
	if json.Unmarshal(live, &liveState) != nil || liveState.Revision == 0 {
		t.Fatal("websocket did not receive updates")
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", base+"/api/sessions/"+snapshot.SessionID+"/events", nil)
	stream, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	stop := errors.New("read complete")
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
		t.Fatalf("history replay missing: count %d, error %v", count, err)
	}
	preview, err := http.Get(snapshot.ResultURL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(preview.Body)
	preview.Body.Close()
	if preview.StatusCode != 200 || !strings.Contains(string(body), "番茄钟") || !strings.Contains(preview.Header.Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("invalid hosted preview, status %d", preview.StatusCode)
	}
	res, err = cloud.CallTool(ctx, &mcp.CallToolParams{Name: "get_status"})
	if err != nil || res.IsError || len(res.Content) == 0 {
		t.Fatalf("status: %v %v", res, err)
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

// POST headers can change on one long-lived SSE connection. GET intentionally
// has no device or conversation identity, matching Linx's transport contract.
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
		conversation := a.conversation
		a.mu.Unlock()
		r.Header.Set("X-Linx-Session-Id", conversation)
		r.Header.Set("X-Linx-Agent-Id", "test-linx-agent")
	}
	return http.DefaultTransport.RoundTrip(r)
}
func (a *linxTransport) setConversation(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.conversation = id
}

type gatedProvider struct {
	agentapi.Provider
	gate  chan struct{}
	sends atomic.Int32
}

func (p *gatedProvider) Create(ctx context.Context) (agentapi.Session, error) {
	select {
	case <-p.gate:
		return p.Provider.Create(ctx)
	case <-ctx.Done():
		return agentapi.Session{}, ctx.Err()
	}
}
func (p *gatedProvider) Send(ctx context.Context, id, text string) error {
	if err := p.Provider.Send(ctx, id, text); err != nil {
		return err
	}
	p.sends.Add(1)
	return nil
}
func voiceResult(t *testing.T, res *mcp.CallToolResult, failed bool) string {
	t.Helper()
	if res == nil || res.IsError != failed || len(res.Content) != 1 || res.StructuredContent != nil {
		t.Fatalf("invalid voice result: %+v", res)
	}
	value, ok := res.Content[0].(*mcp.TextContent)
	if !ok || utf8.RuneCountInString(value.Text) > 60 {
		t.Fatalf("invalid text: %+v", res)
	}
	encoded, _ := json.Marshal(res)
	for _, forbidden := range []string{"structuredContent", "session_id", "result_url", "ses_", "http://", "https://"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("voice result leaked %q: %s", forbidden, encoded)
		}
	}
	return value.Text
}
func TestVoiceProtocolIdentityDedupAndFastAdmission(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	mock := agentapi.NewMock(ctx)
	mock.Step = time.Hour
	p := &gatedProvider{Provider: mock, gate: make(chan struct{})}
	cfg := config.Config{Mode: "http", Token: "test-control-token", DeviceID: "test-device", DataDir: t.TempDir()}
	c := session.New(ctx, p, device.NewState(cfg.DeviceID), &artifacts.Store{Root: cfg.DataDir})
	ts := httptest.NewServer(Handler(cfg, c))
	defer ts.Close()
	defer func() { cancel(); c.Wait() }()
	headers := &linxTransport{conversation: "conversation-a"}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-linx", Version: "1"}, nil)
	cloud, err := client.Connect(ctx, &mcp.SSEClientTransport{Endpoint: ts.URL + "/mcp", HTTPClient: &http.Client{Transport: headers}}, nil)
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
		schema, _ := json.Marshal(tool.InputSchema)
		if strings.Contains(string(schema), "session_id") || strings.Contains(string(schema), "device_id") || tool.OutputSchema != nil {
			t.Fatalf("tool leaks control fields: %+v", tool)
		}
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"get_status", "send_message", "start_new_project"}) {
		t.Fatal(names)
	}
	call := func(name string, args any, failed bool) string {
		t.Helper()
		start := time.Now()
		res, err := cloud.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args, Meta: mcp.Meta{"linxi.control/request-identity": map[string]string{"conversation": "forged"}}})
		if err != nil {
			t.Fatal(err)
		}
		if time.Since(start) >= 300*time.Millisecond {
			t.Fatal("tool blocked on cloud execution")
		}
		return voiceResult(t, res, failed)
	}
	if got := call("get_status", nil, false); got != "还没有任务，可以告诉我想做什么" {
		t.Fatal(got)
	}
	args := map[string]any{"text": "做个页面"}
	first := call("send_message", args, false) // Create remains blocked.
	for range 20 {
		if got := call("send_message", args, false); got != first {
			t.Fatalf("duplicate receipt changed: %s", got)
		}
	}
	// A second SSE connection shares dedup state, including simultaneous retries.
	second, err := client.Connect(ctx, &mcp.SSEClientTransport{Endpoint: ts.URL + "/mcp", HTTPClient: &http.Client{Transport: headers}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	responses := make(chan *mcp.CallToolResult, 8)
	errors := make(chan error, 8)
	for range 8 {
		go func() {
			res, err := second.CallTool(ctx, &mcp.CallToolParams{Name: "send_message", Arguments: args})
			if err != nil {
				errors <- err
				return
			}
			responses <- res
		}()
	}
	for range 8 {
		select {
		case res := <-responses:
			if got := voiceResult(t, res, false); got != first {
				t.Fatalf("duplicate receipt changed on a new connection: %s", got)
			}
		case err := <-errors:
			t.Fatal(err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if got := call("get_status", nil, false); got != "正在准备云端环境" {
		t.Fatal(got)
	}
	// Identity comes from each POST, overriding both GET identity and forged _meta.
	headers.setConversation("conversation-b")
	call("send_message", args, false)
	headers.setConversation("")
	call("send_message", args, false)
	call("send_message", args, false) // No header means no dedup.
	headers.setConversation("conversation-a")
	call("start_new_project", args, false) // Explicit new-project intent has its own key.
	for _, args := range []any{map[string]any{"text": " \n "}, map[string]any{"text": strings.Repeat("字", 2001)}, map[string]any{"text": 42}, map[string]any{"text": "hello", "device_id": "forged"}, map[string]any{}} {
		call("send_message", args, true)
	}
	call("get_status", map[string]any{"session_id": "forged"}, true)
	close(p.gate)
	deadline := time.After(2 * time.Second)
	for p.sends.Load() != 5 {
		select {
		case <-deadline:
			t.Fatalf("dedup/admission: got %d sends, want 5", p.sends.Load())
		case <-time.After(time.Millisecond):
		}
	}
	// The five-second window expires from the original admission, not each retry.
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	call("send_message", args, false)
	deadline = time.After(time.Second)
	for p.sends.Load() != 6 {
		select {
		case <-deadline:
			t.Fatal("dedup entry did not expire")
		case <-time.After(time.Millisecond):
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
