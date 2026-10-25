package devicecontrol

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
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
	clickedID       string
	stopped         int
	captureError    error
	structureError  string
	emptyCapture    bool
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

func (f *fakeRuntime) CaptureScreen(context.Context) (BrowserCapture, error) {
	if f.captureError != nil {
		return BrowserCapture{}, f.captureError
	}
	if f.emptyCapture {
		return BrowserCapture{}, nil
	}
	if f.structureError != "" {
		return BrowserCapture{PNG: []byte("png"), StructureError: f.structureError}, nil
	}
	inspection, _ := f.InspectBrowser(context.Background())
	return BrowserCapture{URL: "https://public.example/display/", Title: "Task", PNG: []byte("png"), Inspection: &inspection}, nil
}

func (f *fakeRuntime) InspectBrowser(context.Context) (BrowserInspection, error) {
	return BrowserInspection{
		URL: "https://public.example/display/", Title: "Task",
		Elements: []BrowserElement{{ID: "e1", Tag: "div", Text: "展开", FrameURL: "https://public.example/preview/"}},
		Frames:   []BrowserFrame{{URL: "https://public.example/preview/", HTML: `<div class="card" data-element-id="e1">展开</div>`}},
	}, nil
}

func (f *fakeRuntime) ClickBrowser(_ context.Context, elementID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clickedID = elementID
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
	if err != nil || len(tools.Tools) != 7 {
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
	inspect, err := peer.CallTool(ctx, &mcp.CallToolParams{Name: "inspect_browser"})
	if err != nil || inspect.IsError {
		t.Fatalf("inspect_browser: %v %v", inspect, err)
	}
	capture, err := peer.CallTool(ctx, &mcp.CallToolParams{Name: "capture_screen"})
	if err != nil || capture.IsError || len(capture.Content) != 2 {
		t.Fatalf("capture_screen: %v %v", capture, err)
	}
	if image, ok := capture.Content[1].(*mcp.ImageContent); !ok || string(image.Data) != "png" {
		t.Fatalf("capture image: %#v", capture.Content[1])
	}
	var captured struct {
		Page *BrowserInspection `json:"page"`
	}
	if err := json.Unmarshal([]byte(capture.Content[0].(*mcp.TextContent).Text), &captured); err != nil || captured.Page == nil || len(captured.Page.Elements) != 1 {
		t.Fatalf("screenshot must include actionable DOM: %+v %v", captured, err)
	}
	if captured.Page.Elements[0].Tag != "div" || len(captured.Page.Frames) != 1 || !strings.Contains(captured.Page.Frames[0].HTML, `data-element-id="e1"`) {
		t.Fatalf("non-button DOM structure lost in MCP response: %+v", captured.Page)
	}
	click, err := peer.CallTool(ctx, &mcp.CallToolParams{
		Name:      "click_browser",
		Arguments: map[string]any{"element_id": "e1"},
	})
	if err != nil || click.IsError {
		t.Fatalf("click_browser: %v %v", click, err)
	}
	runtime.mu.Lock()
	clicked := runtime.clickedID
	runtime.mu.Unlock()
	if clicked != "e1" {
		t.Fatalf("click element: %q", clicked)
	}
	badClick, err := peer.CallTool(ctx, &mcp.CallToolParams{
		Name:      "click_browser",
		Arguments: map[string]any{"element_id": "button"},
	})
	if err != nil || !badClick.IsError {
		t.Fatalf("invalid click element: %v %v", badClick, err)
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

// A failed DOM read must not discard a usable screenshot or pretend that the
// preview has no controls. Conversely, no image must not be reported as success.
func TestDeviceMCPCaptureDegradesExplicitly(t *testing.T) {
	for _, tc := range []struct {
		name    string
		runtime *fakeRuntime
		isError bool
		message string
	}{
		{name: "DOM unavailable", runtime: &fakeRuntime{structureError: "frame disappeared"}, message: "frame disappeared"},
		{name: "browser unavailable", runtime: &fakeRuntime{captureError: errors.New("browser unavailable")}, isError: true, message: "browser unavailable"},
		{name: "empty screenshot", runtime: &fakeRuntime{emptyCapture: true}, isError: true, message: "当前没有可用的浏览器画面"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			server := httptest.NewServer(Handler(tc.runtime))
			defer server.Close()
			client := mcp.NewClient(&mcp.Implementation{Name: "capture-test", Version: "1"}, nil)
			peer, err := client.Connect(ctx, &mcp.SSEClientTransport{Endpoint: server.URL + "/mcp"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer peer.Close()
			result, err := peer.CallTool(ctx, &mcp.CallToolParams{Name: "capture_screen"})
			if err != nil || result.IsError != tc.isError || len(result.Content) == 0 {
				t.Fatalf("capture: %+v %v", result, err)
			}
			message, ok := result.Content[0].(*mcp.TextContent)
			if !ok || !strings.Contains(message.Text, tc.message) {
				t.Fatalf("missing explicit failure: %+v", result.Content[0])
			}
			if !tc.isError {
				var metadata struct {
					Page           *BrowserInspection `json:"page"`
					StructureError string             `json:"structure_error"`
				}
				if err := json.Unmarshal([]byte(message.Text), &metadata); err != nil || metadata.Page != nil || metadata.StructureError != tc.message || len(result.Content) != 2 {
					t.Fatalf("invalid degraded response: %+v %v", metadata, err)
				}
				if image, ok := result.Content[1].(*mcp.ImageContent); !ok || string(image.Data) != "png" {
					t.Fatal("usable screenshot was discarded")
				}
			}
		})
	}
}
