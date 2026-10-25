package devicecontrol

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestDeviceControlValidationHelpers(t *testing.T) {
	for _, raw := range []string{"https://example.test/a", "http://localhost:8080"} {
		if !validURL(raw) {
			t.Fatalf("valid URL rejected: %s", raw)
		}
	}
	for _, raw := range []string{"", "file:///tmp/x", "javascript:alert(1)", "//host/path", "http://"} {
		if validURL(raw) {
			t.Fatalf("invalid URL accepted: %s", raw)
		}
	}
	var input struct {
		Value string `json:"value"`
	}
	if err := decode(json.RawMessage(`{"value":"ok"}`), &input); err != nil || input.Value != "ok" {
		t.Fatalf("decode: %+v %v", input, err)
	}
	if err := decode(json.RawMessage(`{"unknown":true}`), &input); err == nil {
		t.Fatal("unknown field accepted")
	}
	if got := textResult("done", false); got.IsError || len(got.Content) != 1 {
		t.Fatalf("success result: %+v", got)
	}
	if got := textResult("failed", true); !got.IsError || len(got.Content) != 1 {
		t.Fatalf("error result: %+v", got)
	}
}

type fakeRuntime struct {
	mu              sync.Mutex
	castURL         string
	scrollDirection string
	scrollPages     int
	stopped         int
}

func (f *fakeRuntime) ScanScreens(context.Context) ([]Screen, error) {
	return []Screen{{ID: "airplay:screen", Name: "test screen"}}, nil
}

func (f *fakeRuntime) CastScreen(_ context.Context, rawURL string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.castURL = rawURL
	return nil
}

func (f *fakeRuntime) ScrollScreen(_ context.Context, direction string, pages int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scrollDirection = direction
	f.scrollPages = pages
	return nil
}

func (f *fakeRuntime) StopCast(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped++
	return nil
}

func TestDeviceMCPControlsOnlyBrowserAndAirPlay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runtime := &fakeRuntime{}
	server := httptest.NewServer(Handler(runtime))
	defer server.Close()
	request, err := http.NewRequest(http.MethodGet, server.URL+"/mcp", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Forwarded-Prefix", "/device")
	stream, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(stream.Body)
	name, _ := reader.ReadString('\n')
	endpoint, _ := reader.ReadString('\n')
	stream.Body.Close()
	if name != "event: endpoint\n" || !strings.HasPrefix(endpoint, "data: /device/mcp/message?sessionid=") {
		t.Fatalf("device proxy endpoint: %q %q", name, endpoint)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "device-test", Version: "1"}, nil)
	peer, err := client.Connect(ctx, &mcp.SSEClientTransport{Endpoint: server.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()

	tools, err := peer.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 4 {
		t.Fatalf("device tools: %v %v", tools, err)
	}
	if _, err := peer.CallTool(ctx, &mcp.CallToolParams{Name: "scan_screens"}); err != nil {
		t.Fatal(err)
	}
	cast, err := peer.CallTool(ctx, &mcp.CallToolParams{
		Name:      "cast_screen",
		Arguments: map[string]any{"url": "https://public.example/display/?task_ref=task_1"},
	})
	if err != nil || cast.IsError {
		t.Fatalf("cast_screen: %v %v", cast, err)
	}
	runtime.mu.Lock()
	gotURL := runtime.castURL
	runtime.mu.Unlock()
	if gotURL != "https://public.example/display/?task_ref=task_1" {
		t.Fatalf("cast URL: %q", gotURL)
	}
	scroll, err := peer.CallTool(ctx, &mcp.CallToolParams{
		Name:      "scroll_screen",
		Arguments: map[string]any{"direction": "down", "pages": 2},
	})
	if err != nil || scroll.IsError {
		t.Fatalf("scroll_screen: %v %v", scroll, err)
	}
	runtime.mu.Lock()
	direction, pages := runtime.scrollDirection, runtime.scrollPages
	runtime.mu.Unlock()
	if direction != "down" || pages != 2 {
		t.Fatalf("scroll request: %q %d", direction, pages)
	}
	bad, err := peer.CallTool(ctx, &mcp.CallToolParams{
		Name:      "cast_screen",
		Arguments: map[string]any{"url": "file:///etc/passwd"},
	})
	if err != nil || !bad.IsError {
		t.Fatalf("invalid cast URL: %v %v", bad, err)
	}
	if _, err := peer.CallTool(ctx, &mcp.CallToolParams{Name: "stop_cast"}); err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	stopped := runtime.stopped
	runtime.mu.Unlock()
	if stopped != 1 {
		t.Fatalf("stop count: %d", stopped)
	}
}
