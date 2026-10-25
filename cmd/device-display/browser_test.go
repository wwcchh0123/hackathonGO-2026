package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/wwcchh0123/hackathonGO-2026/internal/devicecontrol"
)

func TestBrowserCDPInspectionCaptureAndClick(t *testing.T) {
	var mu sync.Mutex
	var methods []string
	capturePayload := bytes.Repeat([]byte("p"), 64<<10)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/json/list" {
			u, _ := url.Parse(server.URL)
			_, port, _ := net.SplitHostPort(u.Host)
			fmt.Fprintf(w, `[{"type":"page","url":"https://display.test/task","title":"Task preview","webSocketDebuggerUrl":"ws://127.0.0.1:%s/devtools/page/1"}]`, port)
			return
		}
		if r.URL.Path != "/devtools/page/1" {
			http.NotFound(w, r)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "test complete")
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var request struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal(data, &request) != nil {
			return
		}
		mu.Lock()
		methods = append(methods, request.Method)
		mu.Unlock()

		var response any
		switch request.Method {
		case "Page.captureScreenshot":
			response = map[string]any{"id": request.ID, "result": map[string]string{
				"data": base64.StdEncoding.EncodeToString(capturePayload),
			}}
		case "Runtime.evaluate":
			if strings.Contains(string(data), "scrollIntoView") {
				response = map[string]any{"id": request.ID, "result": map[string]any{"result": map[string]any{
					"type": "object", "value": map[string]any{"found": true, "x": 80, "y": 40},
				}}}
			} else {
				response = map[string]any{"id": request.ID, "result": map[string]any{"result": map[string]any{
					"type": "object", "value": devicecontrol.BrowserInspection{
						URL: "https://display.test/task", Title: "Task preview", VisibleText: "展开",
						Elements: []devicecontrol.BrowserElement{{ID: "e1", Tag: "button", Text: "展开"}},
					},
				}}}
			}
		case "Input.dispatchMouseEvent":
			response = map[string]any{"id": request.ID, "result": map[string]any{}}
		default:
			response = map[string]any{"id": request.ID, "error": map[string]string{"message": "unexpected method"}}
		}
		encoded, _ := json.Marshal(response)
		_ = conn.Write(ctx, websocket.MessageText, encoded)
	}))
	defer server.Close()

	u, _ := url.Parse(server.URL)
	_, port, _ := net.SplitHostPort(u.Host)
	debugPort, _ := strconv.Atoi(port)
	b := &browser{debugPort: debugPort, width: 1920, height: 1080}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	capture, err := b.Capture(ctx)
	if err != nil || !bytes.Equal(capture.PNG, capturePayload) || capture.URL == "" {
		t.Fatalf("capture: %#v %v", capture, err)
	}
	inspection, err := b.Inspect(ctx)
	if err != nil || inspection.Title != "Task preview" || len(inspection.Elements) != 1 {
		t.Fatalf("inspect: %#v %v", inspection, err)
	}
	if err := b.Click(ctx, "e1"); err != nil {
		t.Fatalf("click: %v", err)
	}
	if err := b.Click(ctx, "button"); err == nil {
		t.Fatal("invalid element ID accepted")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(methods) != 5 || methods[0] != "Page.captureScreenshot" || methods[1] != "Runtime.evaluate" || methods[2] != "Runtime.evaluate" || methods[3] != "Input.dispatchMouseEvent" || methods[4] != "Input.dispatchMouseEvent" {
		t.Fatalf("CDP methods: %v", methods)
	}
}
