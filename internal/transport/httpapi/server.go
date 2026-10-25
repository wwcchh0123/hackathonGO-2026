// Package httpapi handles wire protocols; session decisions stay in session.
package httpapi

import (
	"bytes"
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/wwcchh0123/hackathonGO-2026/internal/agentapi"
	"github.com/wwcchh0123/hackathonGO-2026/internal/config"
	"github.com/wwcchh0123/hackathonGO-2026/internal/controlplane"
	"github.com/wwcchh0123/hackathonGO-2026/internal/device"
	"github.com/wwcchh0123/hackathonGO-2026/internal/devicedisplay"
	"github.com/wwcchh0123/hackathonGO-2026/internal/events"
	"github.com/wwcchh0123/hackathonGO-2026/internal/session"
	cloudmcp "github.com/wwcchh0123/hackathonGO-2026/internal/transport/mcp"
)

//go:embed devui/* adminui/*
var devUI embed.FS

func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Debug("response disconnected", "error", err)
	}
}
func failure(w http.ResponseWriter, status int, msg string) {
	JSON(w, status, map[string]string{"error": msg})
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return fmt.Errorf("only one JSON value is allowed")
	}
	return nil
}
func same(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }
func Handler(cfg config.Config, c *session.Controller) http.Handler {
	return handler(cfg, c, nil)
}

func HandlerWithManager(cfg config.Config, manager *controlplane.Manager) http.Handler {
	return handler(cfg, manager.DefaultController(), manager)
}

func handler(cfg config.Config, c *session.Controller, manager *controlplane.Manager) http.Handler {
	mux := http.NewServeMux()
	protect := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if cfg.Token != "" {
				if raw := r.Header.Get("Authorization"); raw != "" && !strings.HasPrefix(raw, "Bearer ") {
					failure(w, 401, "需要 Bearer 访问令牌")
					return
				}
				token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				if cookie, err := r.Cookie("linxi_control"); err == nil && token == "" {
					token = cookie.Value
				}
				if !same(token, cfg.Token) {
					failure(w, 401, "需要控制服务访问令牌")
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		JSON(w, 200, map[string]string{"status": "ok", "mode": cfg.Mode})
	})
	mux.HandleFunc("POST /api/connect", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Token string `json:"token"`
		}
		if decode(w, r, &in) != nil {
			failure(w, 400, "无效请求")
			return
		}
		if cfg.Token != "" && !same(in.Token, cfg.Token) {
			failure(w, 401, "访问令牌不正确")
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "linxi_control", Value: in.Token, Path: "/", HttpOnly: true, Secure: strings.HasPrefix(cfg.PublicURL, "https://"), SameSite: http.SameSiteStrictMode})
		JSON(w, 200, map[string]bool{"ok": true})
	})
	mux.Handle("GET /api/bootstrap", protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		JSON(w, 200, map[string]any{"device_id": cfg.DeviceID, "mode": cfg.Mode, "dev_tools": cfg.DevTools})
	})))
	mux.Handle("GET /api/device", protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { JSON(w, 200, c.State.Get()) })))
	mux.Handle("GET /api/device/channel", protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx := conn.CloseRead(r.Context())
		for {
			v, changed := c.State.Watch()
			data, err := json.Marshal(v)
			if err != nil {
				return
			}
			writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err = conn.Write(writeCtx, websocket.MessageText, data)
			cancel()
			if err != nil {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-changed:
			}
		}
	})))
	taskAPI(mux, protect, c)
	// The device browser gets a public, task-scoped URL. It intentionally does
	// not use protect: a browser launched by the hardware cannot safely carry
	// the control token, and the task_ref is only an in-memory read-only key.
	mux.HandleFunc("GET /display", func(w http.ResponseWriter, r *http.Request) {
		target := "/display/"
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusTemporaryRedirect)
	})
	mux.Handle("GET /display/sessions", displaySessions(c))
	mux.Handle("GET /display/events", displayEvents(c))
	mux.Handle("POST /display/preview", displayPreview(c))
	mux.Handle("GET /display/", http.StripPrefix("/display/", devicedisplay.StaticHandler()))
	if manager != nil {
		adminAPI(mux, protect, manager)
	}

	if cfg.DevTools {
		mux.Handle("POST /api/commands", protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var cmd session.Command
			if err := decode(w, r, &cmd); err != nil {
				failure(w, 400, "无效指令 JSON")
				return
			}
			if cmd.Action == "new_project" {
				cmd.Action = "start"
			}
			receipt, err := c.Submit(cmd)
			if err != nil {
				failure(w, 400, err.Error())
				return
			}
			JSON(w, 202, receipt)
		})))
	}
	cloud := cloudmcp.HandlerFor(func(id string) *session.Controller {
		if manager == nil {
			return c
		}
		controller, _ := manager.Controller(id)
		return controller
	}, cfg.PublicURL)
	cloudWithIdentity := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Cloud MCP accepts only its explicit Bearer credential, never browser cookies.
		if cfg.Token != "" && !same(r.Header.Get("Authorization"), "Bearer "+cfg.Token) {
			failure(w, 401, "需要控制服务访问令牌")
			return
		}
		needsDevice := r.Method == http.MethodPost
		if needsDevice && r.URL.Path == "/mcp" {
			// Linx's management console discovers tools before a device exists.
			// Only protocol metadata may omit device identity; tool calls still
			// require the configured device, including read-only get_status.
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<10))
			if err != nil {
				failure(w, 413, "请求过大或无法读取")
				return
			}
			var message struct {
				Method string `json:"method"`
			}
			if json.Unmarshal(body, &message) != nil {
				failure(w, 400, "无效 MCP 请求")
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			switch message.Method {
			case "server/discover", "initialize", "notifications/initialized", "tools/list", "ping":
				needsDevice = false
			}
		}
		if needsDevice {
			id := r.Header.Get("X-Linx-Device-Id")
			if id == "" && cfg.Mode == "mock" {
				id = cfg.DeviceID
			}
			if manager != nil {
				if err := manager.Touch(id, "mcp"); err != nil {
					failure(w, 403, "未知设备")
					return
				}
			} else if id != cfg.DeviceID {
				failure(w, 403, "未知设备")
				return
			}
		}
		cloud.ServeHTTP(w, r)
	})
	mux.Handle("GET /mcp", cloudWithIdentity)
	mux.Handle("POST /mcp", cloudWithIdentity)
	mux.Handle("DELETE /mcp", cloudWithIdentity)
	mux.Handle("POST /mcp/message", cloudWithIdentity)
	results := http.FileServer(http.Dir(cfg.DataDir))
	mux.Handle("GET /results/", http.StripPrefix("/results/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Never expose directory listings of other tasks' preview links.
		rel := strings.TrimPrefix(r.URL.Path, "/")
		if rel == "" {
			rel = "."
		}
		info, err := fs.Stat(os.DirFS(cfg.DataDir), strings.TrimSuffix(rel, "/"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if info.IsDir() {
			if _, err := fs.Stat(os.DirFS(cfg.DataDir), filepath.ToSlash(filepath.Join(rel, "index.html"))); err != nil {
				http.NotFound(w, r)
				return
			}
		}
		w.Header().Set("Content-Security-Policy", "sandbox allow-scripts; default-src 'self' data: blob:; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; connect-src 'none'; form-action 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		results.ServeHTTP(w, r)
	})))
	if cfg.DevTools {
		assets, err := fs.Sub(devUI, "devui")
		if err != nil {
			panic(err)
		} // Build-time embedded directory, not user input.
		mux.Handle("GET /dev-assets/", http.StripPrefix("/dev-assets/", http.FileServer(http.FS(assets))))
		page := func(w http.ResponseWriter, r *http.Request) {
			data, err := devUI.ReadFile("devui/index.html")
			if err != nil {
				http.Error(w, "missing embedded simulator", 500)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(data)
		}
		mux.HandleFunc("GET /simulator", page)
		mux.HandleFunc("GET /device", page)
	}
	if manager != nil {
		assets, err := fs.Sub(devUI, "adminui")
		if err != nil {
			panic(err)
		}
		mux.Handle("GET /admin-assets/", http.StripPrefix("/admin-assets/", http.FileServer(http.FS(assets))))
		page := func(w http.ResponseWriter, r *http.Request) {
			data, err := devUI.ReadFile("adminui/index.html")
			if err != nil {
				http.Error(w, "missing embedded admin console", 500)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(data)
		}
		mux.HandleFunc("GET /admin", page)
	}
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if cfg.DevTools {
			http.Redirect(w, r, "/simulator", http.StatusTemporaryRedirect)
			return
		}
		JSON(w, 200, map[string]string{"service": "linxi-control", "health": "/healthz"})
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		mux.ServeHTTP(w, r)
	})
}

func displayEvents(c *session.Controller) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ref := r.URL.Query().Get("task_ref")
		if ref == "" {
			failure(w, http.StatusBadRequest, "缺少 task_ref")
			return
		}
		zero := 0
		d, err := c.GetTask(r.Context(), ref, session.DetailOptions{EventsLimit: &zero})
		if err != nil {
			taskFailure(w, err)
			return
		}
		observation, err := c.Observe(r.Context(), ref, r.Header.Get("Last-Event-ID"))
		if err != nil {
			taskFailure(w, err)
			return
		}
		defer observation.Close()

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache, no-transform")
		w.Header().Set("X-Accel-Buffering", "no")
		rc := http.NewResponseController(w)
		if rc.Flush() != nil {
			return
		}
		snapshot := displaySnapshot(d)
		if err := writeDisplayEvent(w, rc, "device-snapshot", "", snapshot); err != nil {
			return
		}
		if err := writeDisplayEvent(w, rc, "session-meta", "", map[string]any{
			"taskRef":   ref,
			"sessionId": snapshot.SessionID,
			"runtime":   d.Runtime,
			"source":    "AgentAPI session event stream",
		}); err != nil {
			return
		}
		after := r.Header.Get("Last-Event-ID")
		if after == "" {
			after = r.URL.Query().Get("after")
		}
		err = observation.Run(after, func(f events.Frame) error {
			return writeDisplayEvent(w, rc, "session-event", f.ID, f.Data)
		})
		if err != nil && r.Context().Err() == nil {
			slog.Debug("public display stream closed", "task_ref", ref, "error", err)
		}
	})
}

// displayPreview is the public, read-only display counterpart to the
// authenticated task preview endpoint. The task_ref in a display URL is the
// same short-lived in-memory capability used by /display/events; it contains
// no AgentAPI credential. The browser calls this only after an idle event.
func displayPreview(c *session.Controller) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			TaskRef string `json:"task_ref"`
		}
		if decode(w, r, &in) != nil || in.TaskRef == "" {
			failure(w, http.StatusBadRequest, "无效展示任务")
			return
		}
		result, err := c.Preview(r.Context(), in.TaskRef)
		if err != nil {
			if errors.Is(err, session.ErrReference) || agentapi.IsStatus(err, http.StatusNotFound) {
				taskFailure(w, err)
				return
			}
			// A missing HTML export is expected for some tasks. Return a
			// non-error response so the page can retry while an export settles.
			JSON(w, http.StatusOK, map[string]any{"available": false, "reason": err.Error()})
			return
		}
		JSON(w, http.StatusOK, map[string]any{"available": true, "result_url": result})
	})
}

// displaySessions exposes the minimal task index needed by the public kiosk.
// The browser cannot carry CONTROL_TOKEN, so this endpoint is intentionally
// separate from /api/tasks. It contains task references and presentation data,
// while event contents remain behind the selected task's SSE stream.
func displaySessions(c *session.Controller) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := 20
		if raw := r.URL.Query().Get("limit"); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 || parsed > 50 {
				failure(w, http.StatusBadRequest, "无效展示任务数量")
				return
			}
			limit = parsed
		}
		page, err := c.ListTasks(r.Context(), session.ListOptions{Limit: limit, SkipSummary: true})
		if err != nil {
			taskFailure(w, err)
			return
		}
		items := make([]displaySession, 0, len(page.Data))
		for _, task := range page.Data {
			items = append(items, displaySession{
				TaskRef:   task.Ref,
				SessionID: task.SessionID,
				Title:     task.Title,
				Status:    task.Status,
				Runtime:   task.Runtime,
				CreatedAt: task.CreatedAt,
				UpdatedAt: task.UpdatedAt,
				Archived:  task.Archived,
			})
		}
		JSON(w, http.StatusOK, displaySessionPage{Data: items, HasMore: page.HasMore, NextPage: page.NextPage, QueriedAt: page.QueriedAt})
	})
}

type displaySession struct {
	TaskRef   string    `json:"task_ref"`
	SessionID string    `json:"session_id,omitempty"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
	Runtime   string    `json:"runtime,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Archived  bool      `json:"archived,omitempty"`
}

type displaySessionPage struct {
	Data      []displaySession `json:"data"`
	NextPage  string           `json:"next_page,omitempty"`
	HasMore   bool             `json:"has_more"`
	QueriedAt time.Time        `json:"queried_at"`
}

func displaySnapshot(d session.Detail) device.Snapshot {
	snapshot := device.Snapshot{
		TaskRef:    d.Ref,
		SessionID:  d.SessionID,
		Status:     d.Status,
		Prompt:     d.Title,
		StopReason: d.StopReason,
		ResultURL:  d.ResultURL,
	}
	if d.Summary != nil {
		snapshot.Summary = d.Summary.Text
		snapshot.SummaryEventID = d.Summary.EventID
		snapshot.SummaryTime = &d.Summary.Time
	}
	if d.Command != nil {
		snapshot.CommandID = d.Command.ID
		snapshot.CommandState = d.Command.State
		snapshot.Error = d.Command.Error
	}
	return snapshot
}

func writeDisplayEvent(w http.ResponseWriter, rc *http.ResponseController, name, id string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := rc.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	if id != "" {
		if _, err := fmt.Fprintf(w, "id: %s\n", id); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, data); err != nil {
		return err
	}
	return rc.Flush()
}
