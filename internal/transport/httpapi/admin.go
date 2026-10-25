package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/wwcchh0123/hackathonGO-2026/internal/controlplane"
	"github.com/wwcchh0123/hackathonGO-2026/internal/session"
)

func adminFailure(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, controlplane.ErrUnknownDevice) || errors.Is(err, session.ErrReference) {
		status = http.StatusNotFound
	}
	if errors.Is(err, session.ErrCursorExpired) {
		status = http.StatusConflict
	}
	failure(w, status, err.Error())
}

func adminListOptions(r *http.Request) (session.ListOptions, error) {
	q := r.URL.Query()
	o := session.ListOptions{Page: q.Get("page"), AgentID: q.Get("agent_id"), Status: q.Get("status"), CreatedAfter: q.Get("created_after"), CreatedBefore: q.Get("created_before")}
	if q.Has("limit") {
		n, err := strconv.Atoi(q.Get("limit"))
		if err != nil {
			return o, errors.New("无效 limit")
		}
		o.Limit = n
	}
	if q.Has("include_archived") {
		v, err := strconv.ParseBool(q.Get("include_archived"))
		if err != nil {
			return o, errors.New("无效 include_archived")
		}
		o.IncludeArchived = v
	}
	return o, nil
}

func adminDetailOptions(r *http.Request) (session.DetailOptions, error) {
	q := r.URL.Query()
	o := session.DetailOptions{EventsPage: q.Get("events_page"), FilesPage: q.Get("files_page"), CommandID: q.Get("command_id")}
	if q.Has("events_limit") {
		n, err := strconv.Atoi(q.Get("events_limit"))
		if err != nil {
			return o, errors.New("无效 events_limit")
		}
		o.EventsLimit = &n
	}
	return o, nil
}

func adminDeviceListOptions(r *http.Request) controlplane.DeviceListOptions {
	q := r.URL.Query()
	return controlplane.DeviceListOptions{Query: q.Get("query"), Health: q.Get("health"), Sort: q.Get("sort"), Limit: atoiOrZero(q.Get("limit")), Page: q.Get("page")}
}

func adminTaskListOptions(r *http.Request) controlplane.TaskListOptions {
	q := r.URL.Query()
	return controlplane.TaskListOptions{Query: q.Get("query"), DeviceID: q.Get("device_id"), Status: q.Get("status"), Limit: atoiOrZero(q.Get("limit")), Page: q.Get("page")}
}

func atoiOrZero(raw string) int {
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return -1
	}
	return n
}

func adminAPI(mux *http.ServeMux, protect func(http.Handler) http.Handler, manager *controlplane.Manager) {
	mux.Handle("GET /api/admin/bootstrap", protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		JSON(w, http.StatusOK, map[string]any{"service": "linxi-control", "admin": true, "default_device_id": manager.DefaultController().State.Get().DeviceID, "queried_at": time.Now().UTC()})
	})))
	mux.Handle("GET /api/admin/overview", protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dashboard, err := manager.DashboardQuery(r.Context(), r.URL.Query().Get("query"))
		if err != nil {
			adminFailure(w, err)
			return
		}
		JSON(w, http.StatusOK, dashboard)
	})))
	mux.Handle("GET /api/admin/devices", protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, err := manager.ListDeviceViews(r.Context(), adminDeviceListOptions(r))
		if err != nil {
			adminFailure(w, err)
			return
		}
		JSON(w, http.StatusOK, page)
	})))
	mux.Handle("GET /api/admin/devices/{device_id}", protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		device, err := manager.Overview(r.Context(), r.PathValue("device_id"))
		if err != nil {
			adminFailure(w, err)
			return
		}
		JSON(w, http.StatusOK, map[string]any{"device": device, "queried_at": time.Now().UTC()})
	})))
	mux.Handle("GET /api/admin/devices/{device_id}/snapshot", protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		snapshot, err := manager.Snapshot(r.PathValue("device_id"))
		if err != nil {
			adminFailure(w, err)
			return
		}
		JSON(w, http.StatusOK, map[string]any{"device_id": r.PathValue("device_id"), "snapshot": snapshot, "queried_at": time.Now().UTC()})
	})))
	mux.Handle("GET /api/admin/devices/{device_id}/tasks", protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o, err := adminListOptions(r)
		if err != nil {
			adminFailure(w, err)
			return
		}
		page, err := manager.ListTasks(r.Context(), r.PathValue("device_id"), o)
		if err != nil {
			adminFailure(w, err)
			return
		}
		JSON(w, http.StatusOK, map[string]any{"device_id": r.PathValue("device_id"), "tasks": page})
	})))
	mux.Handle("GET /api/admin/devices/{device_id}/tasks/{ref}", protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o, err := adminDetailOptions(r)
		if err != nil {
			adminFailure(w, err)
			return
		}
		detail, err := manager.GetTask(r.Context(), r.PathValue("device_id"), r.PathValue("ref"), o)
		if err != nil {
			adminFailure(w, err)
			return
		}
		JSON(w, http.StatusOK, map[string]any{"device_id": r.PathValue("device_id"), "task": detail})
	})))
	mux.Handle("GET /api/admin/tasks", protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, err := manager.ListAdminTasks(r.Context(), adminTaskListOptions(r))
		if err != nil {
			adminFailure(w, err)
			return
		}
		JSON(w, http.StatusOK, page)
	})))
	mux.Handle("GET /api/admin/tasks/{device_id}/{ref}", protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o, err := adminDetailOptions(r)
		if err != nil {
			adminFailure(w, err)
			return
		}
		detail, err := manager.GetTask(r.Context(), r.PathValue("device_id"), r.PathValue("ref"), o)
		if err != nil {
			adminFailure(w, err)
			return
		}
		JSON(w, http.StatusOK, map[string]any{"device_id": r.PathValue("device_id"), "task": detail})
	})))
}
