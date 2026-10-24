package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "test-linx", Version: "1"}, nil)
	cloud, err := mcpClient.Connect(ctx, &mcp.SSEClientTransport{Endpoint: base + "/mcp", HTTPClient: client}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cloud.Close()
	tools, err := cloud.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 5 {
		t.Fatalf("tools discovery: %v %v", tools, err)
	}
	res, err := cloud.CallTool(ctx, &mcp.CallToolParams{Name: "start_task", Arguments: map[string]any{"prompt": "帮我做个番茄钟"}})
	if err != nil || res.IsError {
		t.Fatalf("start_task: %v %v", res, err)
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
	res, err = cloud.CallTool(ctx, &mcp.CallToolParams{Name: "show", Arguments: map[string]any{"target": "preview"}})
	if err != nil || res.IsError {
		t.Fatalf("show preview: %v %v", res, err)
	}
	for c.State.Get().View != device.Preview {
		select {
		case <-ctx.Done():
			t.Fatal("view switch did not reach device")
		case <-time.After(time.Millisecond):
		}
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
