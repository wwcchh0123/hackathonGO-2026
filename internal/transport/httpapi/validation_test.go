package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wwcchh0123/hackathonGO-2026/internal/agentapi"
	"github.com/wwcchh0123/hackathonGO-2026/internal/config"
	"github.com/wwcchh0123/hackathonGO-2026/internal/controlplane"
	"github.com/wwcchh0123/hackathonGO-2026/internal/session"
)

func TestHTTPRejectsMalformedInputsBeforeMutatingTasks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	mock := agentapi.NewMock(ctx)
	manager := controlplane.New(ctx, mock, "device-a", t.TempDir(), "https://work.example")
	defer func() { cancel(); manager.Wait() }()
	cfg := config.Config{Mode: "http", DeviceID: "device-a", Token: "secret-control", PublicURL: "https://work.example", DevTools: true, DataDir: t.TempDir()}
	handler := HandlerWithManager(cfg, manager)
	for _, test := range []struct {
		method, path, body string
		want               int
	}{
		{"POST", "/api/connect", "{", 400},
		{"POST", "/api/connect", `{"token":"wrong"}`, 401},
		{"POST", "/api/connect", `{"token":"secret-control"} {}`, 400},
		{"POST", "/api/commands", "{", 400},
		{"POST", "/api/commands", `{"action":"invalid"}`, 400},
		{"GET", "/api/tasks?limit=bad", "", 400},
		{"GET", "/api/tasks?include_archived=bad", "", 400},
		{"GET", "/api/tasks?status=bad", "", 400},
		{"GET", "/api/tasks?created_after=bad", "", 400},
		{"GET", "/api/tasks/missing?events_limit=bad", "", 400},
		{"POST", "/api/device/view", "{", 400},
		{"POST", "/api/device/view", `{"task_ref":"missing"}`, 404},
		{"POST", "/api/tasks/missing/preview", "{}", 404},
		{"GET", "/api/tasks/missing/events", "", 404},
		{"GET", "/api/sessions/missing/events", "", 404},
		{"GET", "/display/events", "", 400},
		{"POST", "/display/preview", "{", 400},
		{"POST", "/display/preview", "{}", 400},
		{"POST", "/display/preview", `{"task_ref":"missing"}`, 404},
		{"GET", "/api/admin/devices?sort=bad", "", 400},
		{"GET", "/api/admin/devices?limit=bad", "", 400},
		{"GET", "/api/admin/tasks?status=bad", "", 400},
		{"GET", "/api/admin/devices/missing", "", 404},
		{"GET", "/api/admin/devices/missing/snapshot", "", 404},
		{"GET", "/api/admin/devices/missing/tasks", "", 404},
		{"GET", "/api/admin/devices/device-a/tasks?limit=bad", "", 400},
		{"GET", "/api/admin/devices/device-a/tasks?include_archived=bad", "", 400},
		{"GET", "/api/admin/devices/device-a/tasks/missing?events_limit=bad", "", 400},
		{"GET", "/api/admin/devices/device-a/tasks/missing", "", 404},
		{"GET", "/api/admin/tasks/device-a/missing?events_limit=bad", "", 400},
		{"GET", "/api/admin/tasks/device-a/missing", "", 404},
		{"GET", "/not-found", "", 404},
		{"GET", "/", "", 307},
		{"GET", "/display?task_ref=task-a", "", 307},
		{"POST", "/mcp", "not-json", 400},
		{"POST", "/mcp", strings.Repeat("x", 33<<10), 413},
	} {
		t.Run(test.method+test.path+test.body[:min(len(test.body), 20)], func(t *testing.T) {
			request := httptest.NewRequest(test.method, "http://localhost"+test.path, strings.NewReader(test.body))
			request.Header.Set("Authorization", "Bearer "+cfg.Token)
			request.Header.Set("X-Linx-Device-Id", "device-a")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.want, response.Body)
			}
		})
	}
	page, err := manager.DefaultController().ListTasks(ctx, session.ListOptions{})
	if err != nil || len(page.Data) != 0 {
		t.Fatalf("invalid input created tasks: %+v %v", page, err)
	}
	for _, auth := range []string{"Basic credential", "Bearer wrong"} {
		request := httptest.NewRequest("GET", "http://localhost/api/bootstrap", nil)
		request.Header.Set("Authorization", auth)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != 401 {
			t.Fatalf("invalid auth accepted: %s", auth)
		}
	}
	request := httptest.NewRequest("POST", "http://localhost/api/connect", strings.NewReader(`{"token":"secret-control"}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	cookies := response.Result().Cookies()
	if response.Code != 200 || len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly {
		t.Fatalf("cookie: %+v status=%d", cookies, response.Code)
	}
}

func TestTaskFailurePreservesErrorCategories(t *testing.T) {
	for _, test := range []struct {
		err  error
		want int
	}{
		{session.ErrReference, 404}, {&agentapi.Error{StatusCode: 404}, 404},
		{&agentapi.Error{StatusCode: 401}, 403}, {&agentapi.Error{StatusCode: 403}, 403},
		{session.ErrCursorExpired, 409}, {context.DeadlineExceeded, 504},
		{&agentapi.Error{StatusCode: 503}, 502}, {errors.New("invalid"), 400},
	} {
		response := httptest.NewRecorder()
		taskFailure(response, test.err)
		if response.Code != test.want {
			t.Fatalf("%v: status=%d want=%d", test.err, response.Code, test.want)
		}
	}
}

func TestPublicPreviewReportsUnavailableWithoutClaimingCompletion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	mock := agentapi.NewMock(ctx)
	mock.Step = time.Hour
	manager := controlplane.New(ctx, mock, "device-a", t.TempDir(), "http://work.example")
	defer func() { cancel(); manager.Wait() }()
	c := manager.DefaultController()
	receipt, err := c.Submit(session.Command{Action: "start", Prompt: "long-running task"})
	if err != nil {
		t.Fatal(err)
	}
	waitDetail(t, c, receipt)
	response := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "http://localhost/display/preview", strings.NewReader(`{"task_ref":"`+receipt.TaskRef+`"}`))
	displayPreview(c).ServeHTTP(response, request)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"available":false`) {
		t.Fatalf("running preview: %d %s", response.Code, response.Body)
	}
}

type failedResponseWriter struct{ header http.Header }

func (w *failedResponseWriter) Header() http.Header            { return w.header }
func (*failedResponseWriter) WriteHeader(int)                  {}
func (*failedResponseWriter) Write([]byte) (int, error)        { return 0, io.ErrClosedPipe }
func (*failedResponseWriter) SetWriteDeadline(time.Time) error { return nil }
func (*failedResponseWriter) FlushError() error                { return io.ErrClosedPipe }

func TestSSEDisconnectAndEncodingFailuresAreReturned(t *testing.T) {
	failed := &failedResponseWriter{header: make(http.Header)}
	JSON(failed, 200, map[string]bool{"ok": true})
	for _, value := range []any{make(chan int), map[string]string{"text": "hello"}} {
		if err := writeDisplayEvent(failed, http.NewResponseController(failed), "event", "event-id", value); err == nil {
			t.Fatal("SSE error swallowed")
		}
	}
	if err := writeDisplayEvent(httptest.NewRecorder(), http.NewResponseController(httptest.NewRecorder()), "event", "", map[string]bool{"ok": true}); err == nil {
		t.Fatal("unsupported writer should return deadline error")
	}
}
