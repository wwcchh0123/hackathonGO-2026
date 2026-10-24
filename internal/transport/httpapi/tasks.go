package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/wwcchh0123/hackathonGO-2026/internal/agentapi"
	"github.com/wwcchh0123/hackathonGO-2026/internal/events"
	"github.com/wwcchh0123/hackathonGO-2026/internal/session"
)

func taskFailure(w http.ResponseWriter, err error) {
	status := 400
	if errors.Is(err, session.ErrReference) || agentapi.IsStatus(err, 404) {
		status = 404
	}
	if agentapi.IsStatus(err, 401) || agentapi.IsStatus(err, 403) {
		status = 403
	}
	if errors.Is(err, session.ErrCursorExpired) {
		status = 409
	}
	if errors.Is(err, context.DeadlineExceeded) {
		status = 504
	} else if agentapi.Retryable(err) {
		status = 502
	}
	failure(w, status, err.Error())
}
func taskAPI(mux *http.ServeMux, protect func(http.Handler) http.Handler, c *session.Controller) {
	mux.Handle("GET /api/tasks", protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		n := 0
		var err error
		if q.Has("limit") {
			n, err = strconv.Atoi(q.Get("limit"))
		}
		if err != nil {
			failure(w, 400, "无效 limit")
			return
		}
		archived := false
		if q.Has("include_archived") {
			archived, err = strconv.ParseBool(q.Get("include_archived"))
			if err != nil {
				failure(w, 400, "无效 include_archived")
				return
			}
		}
		page, err := c.ListTasks(r.Context(), session.ListOptions{Limit: n, Page: q.Get("page"), AgentID: q.Get("agent_id"), Status: q.Get("status"), IncludeArchived: archived, CreatedAfter: q.Get("created_after"), CreatedBefore: q.Get("created_before")})
		if err != nil {
			taskFailure(w, err)
			return
		}
		JSON(w, 200, page)
	})))
	mux.Handle("GET /api/tasks/{ref}", protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		o := session.DetailOptions{EventsPage: q.Get("events_page"), FilesPage: q.Get("files_page"), CommandID: q.Get("command_id")}
		if q.Has("events_limit") {
			n, err := strconv.Atoi(q.Get("events_limit"))
			if err != nil {
				failure(w, 400, "无效 events_limit")
				return
			}
			o.EventsLimit = &n
		}
		d, err := c.GetTask(r.Context(), r.PathValue("ref"), o)
		if err != nil {
			taskFailure(w, err)
			return
		}
		JSON(w, 200, d)
	})))
	mux.Handle("POST /api/device/view", protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			TaskRef string `json:"task_ref"`
		}
		if decode(w, r, &in) != nil {
			failure(w, 400, "无效展示选择")
			return
		}
		d, err := c.SelectView(r.Context(), in.TaskRef)
		if err != nil {
			taskFailure(w, err)
			return
		}
		JSON(w, 200, d)
	})))
	mux.Handle("POST /api/tasks/{ref}/preview", protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result, err := c.Preview(r.Context(), r.PathValue("ref"))
		if err != nil {
			taskFailure(w, err)
			return
		}
		JSON(w, 200, map[string]string{"result_url": result})
	})))
	stream := func(w http.ResponseWriter, r *http.Request, ref string) {
		after := r.Header.Get("Last-Event-ID")
		if after == "" {
			after = r.URL.Query().Get("after")
		}
		observation, err := c.Observe(r.Context(), ref, after)
		if err != nil {
			taskFailure(w, err)
			return
		}
		defer observation.Close()
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		rc := http.NewResponseController(w)
		if rc.Flush() != nil {
			return
		}
		err = observation.Run(after, func(f events.Frame) error {
			if err := rc.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
				return err
			}
			if _, err := fmt.Fprintf(w, "id: %s\nevent: agent_event\ndata: %s\n\n", f.ID, strings.ReplaceAll(string(f.Data), "\n", "\ndata: ")); err != nil {
				return err
			}
			return rc.Flush()
		})
		if err != nil && r.Context().Err() == nil {
			_ = rc.SetWriteDeadline(time.Now().Add(time.Second))
			_, _ = fmt.Fprint(w, "event: stream_error\ndata: {\"resync_required\":true}\n\n")
			_ = rc.Flush()
		}
	}
	mux.Handle("GET /api/tasks/{ref}/events", protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { stream(w, r, r.PathValue("ref")) })))
	// Compatibility for the independently developed device display (#12): only
	// the explicitly selected Session is addressable by its raw ID.
	mux.Handle("GET /api/sessions/{id}/events", protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		view := c.State.Get()
		if view.SessionID != r.PathValue("id") || view.TaskRef == "" {
			failure(w, 404, "请先明确选择展示任务")
			return
		}
		stream(w, r, view.TaskRef)
	})))
}
