package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wwcchh0123/hackathonGO-2026/internal/agentapi"
	"github.com/wwcchh0123/hackathonGO-2026/internal/artifacts"
	"github.com/wwcchh0123/hackathonGO-2026/internal/device"
	"github.com/wwcchh0123/hackathonGO-2026/internal/session"
)

func TestDisplayURLUsesPublicSessionPage(t *testing.T) {
	if got := displayURL("https://work.example/", "task_中文"); got != "https://work.example/display/?task_ref=task_%E4%B8%AD%E6%96%87" {
		t.Fatalf("display URL: %q", got)
	}
	if got := displayURL("https://demo.ngrok-free.dev/", "task_1"); got != "https://demo.ngrok-free.dev/display/?ngrok-skip-browser-warning=true&task_ref=task_1" {
		t.Fatalf("ngrok display URL: %q", got)
	}
	if got := displayURL("", "ses_1"); got != "" {
		t.Fatalf("empty public URL should disable display links: %q", got)
	}
}

func TestReceiptIncludesTaskDisplayURL(t *testing.T) {
	value := withDisplayURL(session.Receipt{TaskRef: "task_existing"}, "https://work.example")
	raw, err := json.Marshal(value)
	if err != nil || !strings.Contains(string(raw), "https://work.example/display/?task_ref=task_existing") {
		t.Fatalf("display URL missing: %s (%v)", raw, err)
	}
}

func TestHandlerServesAllToolsAndRoutesIdentity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mock := agentapi.NewMock(ctx)
	mock.Step = 0
	controller := session.New(ctx, mock, device.NewState("device-a"), &artifacts.Store{Root: t.TempDir()}, session.Options{PollInterval: time.Millisecond, RequestTimeout: time.Second, StopTimeout: time.Second})
	defer func() { cancel(); controller.Wait() }()
	server := httptest.NewServer(HandlerFor(func(deviceID string) *session.Controller {
		if deviceID == "device-a" {
			return controller
		}
		return nil
	}, "https://work.example"))
	defer server.Close()
	peer, err := mcp.NewClient(&mcp.Implementation{Name: "control-test", Version: "1"}, nil).Connect(ctx, &mcp.SSEClientTransport{Endpoint: server.URL + "/mcp", HTTPClient: &http.Client{Transport: identityTransport{}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	tools, err := peer.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 4 {
		t.Fatalf("tools: %v %+v", err, tools)
	}
	meta := mcp.Meta{identityMeta: map[string]string{"device": "device-a", "conversation": "conversation-a", "agent": "agent-a"}}
	newTask, err := peer.CallTool(ctx, &mcp.CallToolParams{Name: "new_task", Arguments: map[string]any{"text": "做一个页面"}, Meta: meta})
	if err != nil || newTask.IsError {
		t.Fatalf("new_task: %v %+v", err, newTask)
	}
	var receipt session.Receipt
	if err := json.Unmarshal([]byte(newTask.Content[0].(*mcp.TextContent).Text), &receipt); err != nil || receipt.TaskRef == "" {
		t.Fatalf("receipt: %v %+v", err, receipt)
	}
	if !strings.Contains(newTask.Content[0].(*mcp.TextContent).Text, "https://work.example/display/") {
		t.Fatalf("display URL missing: %s", newTask.Content[0].(*mcp.TextContent).Text)
	}
	list, err := peer.CallTool(ctx, &mcp.CallToolParams{Name: "list_tasks", Arguments: map[string]any{"limit": 10}, Meta: meta})
	if err != nil || list.IsError {
		t.Fatalf("list_tasks: %v %+v", err, list)
	}
	detail, err := peer.CallTool(ctx, &mcp.CallToolParams{Name: "get_task", Arguments: map[string]any{"task_ref": receipt.TaskRef, "events_limit": 0}, Meta: meta})
	if err != nil || detail.IsError {
		t.Fatalf("get_task: %v %+v", err, detail)
	}
	send, err := peer.CallTool(ctx, &mcp.CallToolParams{Name: "send_message", Arguments: map[string]any{"text": "继续", "task_ref": receipt.TaskRef}, Meta: meta})
	if err != nil || send.IsError {
		t.Fatalf("send_message: %v %+v", err, send)
	}
	badArgs, err := peer.CallTool(ctx, &mcp.CallToolParams{Name: "new_task", Arguments: map[string]any{"text": "x", "task_ref": "wrong"}, Meta: meta})
	if err != nil || !badArgs.IsError {
		t.Fatalf("invalid argument result: %v %+v", err, badArgs)
	}
}

// Each HTTP request carries the current device identity, independently of client metadata.
type identityTransport struct{}

func (identityTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("X-Linx-Device-Id", "device-a")
	r.Header.Set("X-Linx-Session-Id", "conversation-a")
	r.Header.Set("X-Linx-Agent-Id", "agent-a")
	return http.DefaultTransport.RoundTrip(r)
}

func TestMCPDecodingAndUnknownDeviceFailures(t *testing.T) {
	for _, raw := range []string{"null", "{", `{"unknown":true}`, `{} {}`} {
		var input struct{}
		if err := decodeArgs(json.RawMessage(raw), &input); err == nil {
			t.Fatalf("invalid arguments accepted: %s", raw)
		}
	}
	var input struct{}
	if err := decodeArgs(nil, &input); err != nil {
		t.Fatal(err)
	}
	if got := displayURL("http://[invalid", "task-a"); got != "" {
		t.Fatalf("invalid URL accepted: %s", got)
	}
	h := HandlerFor(func(string) *session.Controller { return nil }, "")
	for _, name := range []string{"list_tasks", "get_task", "send_message", "new_task"} {
		args := map[string]any{}
		if name == "get_task" {
			args["task_ref"] = "task-a"
		}
		if name == "send_message" || name == "new_task" {
			args["text"] = "hello"
		}
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
		req := httptest.NewRequest("POST", "http://localhost/mcp", strings.NewReader(string(body)))
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Mcp-Protocol-Version", "2025-06-18")
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		if res.Code != 200 || !strings.Contains(res.Body.String(), `"isError":true`) {
			t.Fatalf("%s: %d %s", name, res.Code, res.Body)
		}
	}
	req := httptest.NewRequest("POST", "http://localhost/mcp", strings.NewReader(strings.Repeat("x", 33<<10)))
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != 413 {
		t.Fatalf("oversized body status=%d", res.Code)
	}
}
