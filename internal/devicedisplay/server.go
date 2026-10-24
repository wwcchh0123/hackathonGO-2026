package devicedisplay

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/wwcchh0123/hackathonGO-2026/internal/device"
)

//go:embed web/*
var webFiles embed.FS

type View struct {
	store         *Store
	airplayTarget string
}

func NewView(store *Store, airplayTarget string) *View {
	return &View{store: store, airplayTarget: airplayTarget}
}

// Handler serves only the local renderer and its local event stream. The
// control token is never exposed to this HTTP server or to the browser.
func (v *View) Handler() http.Handler {
	web, err := fs.Sub(webFiles, "web")
	if err != nil {
		panic(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("GET /api/session", func(w http.ResponseWriter, r *http.Request) {
		snapshot, events := v.store.Snapshot()
		writeJSON(w, http.StatusOK, v.meta(snapshot, events))
	})
	mux.HandleFunc("GET /api/events", v.events)
	mux.Handle("/", http.FileServer(http.FS(web)))
	return mux
}

// events exposes a second, local-only SSE stream. The device client consumes
// the authenticated control-service stream; Chromium consumes this stream so
// the renderer never needs the control token.
func (v *View) events(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming is unavailable", http.StatusInternalServerError)
		return
	}
	// Subscribe before taking the snapshot. That closes the history/live race:
	// an event arriving between these two operations is queued and then safely
	// ignored by the browser's event-ID de-duplication if it was in history.
	updates, unsubscribe := v.store.Subscribe()
	defer unsubscribe()
	snapshot, events := v.store.Snapshot()
	currentSession := snapshot.SessionID
	writeSSE(w, "device-snapshot", snapshot)
	writeSSE(w, "session-meta", v.meta(snapshot, events))
	for _, event := range events {
		writeRawSSE(w, "session-event", event.ID, event.Data)
	}
	flusher.Flush()

	keepAlive := time.NewTicker(15 * time.Second)
	defer keepAlive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepAlive.C:
			_, _ = fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		case update := <-updates:
			switch update.Kind {
			case "reset":
				currentSession = update.Snapshot.SessionID
				writeSSE(w, "session-reset", update.Snapshot)
				writeSSE(w, "device-snapshot", update.Snapshot)
				writeSSE(w, "session-meta", v.meta(update.Snapshot, nil))
			case "snapshot":
				writeSSE(w, "device-snapshot", update.Snapshot)
			case "event":
				if update.Session == currentSession {
					writeRawSSE(w, "session-event", update.Event.ID, update.Event.Data)
				}
			}
			flusher.Flush()
		}
	}
}

func (v *View) meta(snapshot device.Snapshot, events []Event) map[string]any {
	// The control-service SSE intentionally contains only raw events. This
	// lightweight metadata is derived locally for the visual header and does
	// not become a second protocol or a source of business state.
	meta := map[string]any{
		"sessionId":        snapshot.SessionID,
		"runtime":          "claudecode",
		"source":           "control service SSE",
		"eventCount":       len(events),
		"typeCounts":       map[string]int{},
		"startedAt":        nil,
		"endedAt":          nil,
		"durationMs":       nil,
		"replayIntervalMs": 0,
		"loop":             false,
		"airplayTarget":    v.airplayTarget,
	}
	var first, last time.Time
	for _, event := range events {
		var value struct {
			Type        string `json:"type"`
			ProcessedAt string `json:"processed_at"`
			Runtime     string `json:"runtime"`
		}
		if json.Unmarshal(event.Data, &value) != nil {
			continue
		}
		if value.Type != "" {
			counts := meta["typeCounts"].(map[string]int)
			counts[value.Type]++
		}
		if value.Runtime != "" {
			meta["runtime"] = value.Runtime
		}
		if timestamp, err := time.Parse(time.RFC3339Nano, value.ProcessedAt); err == nil {
			if first.IsZero() {
				first = timestamp
			}
			last = timestamp
		}
	}
	if !first.IsZero() {
		meta["startedAt"] = first.Format(time.RFC3339Nano)
		meta["endedAt"] = last.Format(time.RFC3339Nano)
		meta["durationMs"] = last.Sub(first).Milliseconds()
	}
	return meta
}

func writeSSE(w http.ResponseWriter, event string, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		return
	}
	writeRawSSE(w, event, "", data)
}

func writeRawSSE(w http.ResponseWriter, event, id string, data []byte) {
	if id != "" {
		_, _ = fmt.Fprintf(w, "id: %s\n", id)
	}
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, strings.TrimSpace(string(data)))
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
