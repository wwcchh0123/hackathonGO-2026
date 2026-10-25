package voice

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Presentation is a device-local snapshot, never a second Lingxi session.
type Presentation struct {
	Connected bool    `json:"connected"`
	Phase     string  `json:"phase"`
	Text      string  `json:"text"`
	UserText  string  `json:"user_text"`
	Level     float64 `json:"level"`
	Turn      uint64  `json:"turn_id"`
	ViewURL   string  `json:"view_url"`
}

type stageHub struct {
	mu    sync.Mutex
	state Presentation
	subs  map[chan Presentation]struct{}
}

func (h *stageHub) snapshot() Presentation {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.snapshotLocked()
}

func (h *stageHub) snapshotLocked() Presentation {
	s := h.state
	if s.Phase == "" {
		s.Phase = "offline"
	}
	return s
}

func (h *stageHub) update(fn func(*Presentation)) Presentation {
	h.mu.Lock()
	defer h.mu.Unlock()
	before := h.state
	fn(&h.state)
	s := h.snapshotLocked()
	if before != h.state {
		for ch := range h.subs {
			// Only the latest frame matters; a slow screen never blocks speech.
			select {
			case <-ch:
			default:
			}
			ch <- s
		}
	}
	return s
}

func (h *stageHub) reset(phase string, connected bool) uint64 {
	return h.update(func(s *Presentation) {
		s.Turn++
		s.Phase, s.Connected, s.Level = phase, connected, 0
		s.Text, s.UserText = "", ""
	}).Turn
}

func (h *stageHub) playback(turn uint64, state string, level float64, text string) {
	h.update(func(s *Presentation) {
		if turn == 0 || turn != s.Turn || !s.Connected || s.Phase == "listening" {
			return
		}
		switch state {
		case "playing":
			s.Phase, s.Text = "speaking", boundedText(text)
			if math.IsNaN(level) || math.IsInf(level, 0) {
				level = 0
			}
			s.Level = math.Max(0, math.Min(1, level))
		case "buffering":
			s.Level = 0
		case "idle":
			s.Phase, s.Level = "idle", 0
		}
	})
}

func boundedText(text string) string {
	runes := []rune(text)
	if len(runes) > 600 {
		runes = runes[:600]
	}
	return string(runes)
}

func (h *stageHub) subscribe() (chan Presentation, func(), bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.subs) >= 8 {
		return nil, nil, false
	}
	if h.subs == nil {
		h.subs = make(map[chan Presentation]struct{})
	}
	ch := make(chan Presentation, 1)
	h.subs[ch] = struct{}{}
	ch <- h.snapshotLocked()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
	}, true
}

func (s *Server) stageEvents(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host {
		http.Error(w, "same-origin screen required", http.StatusForbidden)
		return
	}
	ch, unsubscribe, ok := s.stage.subscribe()
	if !ok {
		http.Error(w, "too many screens", http.StatusTooManyRequests)
		return
	}
	defer unsubscribe()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	rc := http.NewResponseController(w)
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for {
		var frame string
		select {
		case <-r.Context().Done():
			return
		case v := <-ch:
			b, _ := json.Marshal(v)
			frame = "data: " + string(b) + "\n\n"
		case <-tick.C:
			frame = ": keepalive\n\n"
		}
		_ = rc.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.WriteString(w, frame); err != nil {
			return
		}
		if err := rc.Flush(); err != nil {
			return
		}
	}
}

func stageMutationAllowed(r *http.Request) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		return origin == "http://"+r.Host
	}
	return r.Header.Get("X-Linxi-Device") == "1"
}

func (s *Server) stageView(w http.ResponseWriter, r *http.Request) {
	if !stageMutationAllowed(r) {
		http.Error(w, "same-origin device request required", http.StatusForbidden)
		return
	}
	var input struct {
		URL string `json:"url"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	d.DisallowUnknownFields()
	if err := d.Decode(&input); err != nil {
		http.Error(w, "invalid view", http.StatusBadRequest)
		return
	}
	if input.URL != "" {
		u, err := url.Parse(input.URL)
		allowed := false
		if err == nil && u.User == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
			for _, host := range s.DisplayAllowedHosts {
				if strings.EqualFold(u.Hostname(), strings.TrimSpace(host)) {
					allowed = true
				}
			}
		}
		if !allowed {
			http.Error(w, "display host is not allowed", http.StatusForbidden)
			return
		}
	}
	s.stage.update(func(p *Presentation) { p.ViewURL = input.URL })
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) wakeDisplay(w http.ResponseWriter, r *http.Request) {
	if !stageMutationAllowed(r) {
		http.Error(w, "same-origin device request required", http.StatusForbidden)
		return
	}
	if s.DisplayWakeURL == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.DisplayWakeURL, nil)
	if err != nil {
		http.Error(w, "invalid display wake URL", http.StatusServiceUnavailable)
		return
	}
	req.Header.Set("X-Linxi-Device", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, "投屏服务暂不可用，语音仍可继续", http.StatusServiceUnavailable)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		http.Error(w, fmt.Sprintf("投屏启动失败（HTTP %d）", resp.StatusCode), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
