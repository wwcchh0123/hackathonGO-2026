// Package httpapi handles wire protocols; session decisions stay in session.
package httpapi

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/wwcchh0123/hackathonGO-2026/internal/config"
	"github.com/wwcchh0123/hackathonGO-2026/internal/events"
	"github.com/wwcchh0123/hackathonGO-2026/internal/session"
	cloudmcp "github.com/wwcchh0123/hackathonGO-2026/internal/transport/mcp"
)

//go:embed devui/*
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
	mux := http.NewServeMux()
	protect := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if cfg.Token != "" {
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
	mux.Handle("GET /api/sessions/{id}/events", protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id != c.State.Get().SessionID {
			failure(w, 404, "当前设备未绑定此会话")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		rc := http.NewResponseController(w)
		if err := rc.Flush(); err != nil {
			return
		}
		// Per-write deadlines bound a stalled device without timing out an idle stream.
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		err := c.Events.Replay(ctx, id, r.Header.Get("Last-Event-ID"), func(f events.Frame) error {
			if err := rc.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
				return err
			}
			if _, err := fmt.Fprintf(w, "id: %s\nevent: agent_event\ndata: %s\n\n", f.ID, f.Data); err != nil {
				return err
			}
			return rc.Flush()
		})
		if err != nil && r.Context().Err() == nil {
			slog.Debug("device SSE closed", "error", err)
		}
	})))
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
	cloud := cloudmcp.Handler(c)
	cloudWithIdentity := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Cloud MCP accepts only its explicit Bearer credential, never browser cookies.
		if cfg.Token != "" && !same(r.Header.Get("Authorization"), "Bearer "+cfg.Token) {
			failure(w, 401, "需要控制服务访问令牌")
			return
		}
		if r.Method == http.MethodPost {
			id := r.Header.Get("X-Linx-Device-Id")
			if id == "" && cfg.Mode == "mock" {
				id = cfg.DeviceID
			}
			if id != cfg.DeviceID {
				failure(w, 403, "未知设备")
				return
			}
		}
		cloud.ServeHTTP(w, r)
	})
	mux.Handle("GET /mcp", cloudWithIdentity)
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
