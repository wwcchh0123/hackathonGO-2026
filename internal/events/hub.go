// Package events stores raw provider events for gap-free history + live delivery.
package events

import (
	"context"
	"encoding/json"
	"sync"
)

type Frame struct {
	ID   string
	Type string
	Data json.RawMessage
}

type log struct {
	frames []Frame
	seen   map[string]bool
	wake   chan struct{}
}

// Hub is deliberately in-memory for the single-device prototype.
// Notification channels only signal changes; subscribers read the actual log,
// so a slow subscriber never loses events at the history/live boundary.
type Hub struct {
	mu   sync.Mutex
	logs map[string]*log
}

func NewHub() *Hub { return &Hub{logs: make(map[string]*log)} }
func (h *Hub) get(id string) *log {
	l := h.logs[id]
	if l == nil {
		l = &log{seen: make(map[string]bool), wake: make(chan struct{})}
		h.logs[id] = l
	}
	return l
}
func (h *Hub) Append(session string, f Frame) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	l := h.get(session)
	if f.ID != "" && l.seen[f.ID] {
		return false
	}
	if f.ID != "" {
		l.seen[f.ID] = true
	}
	f.Data = append(json.RawMessage(nil), f.Data...)
	l.frames = append(l.frames, f)
	close(l.wake)
	l.wake = make(chan struct{})
	return true
}

// Replay starts after a known ID; an unknown cursor replays everything.
func (h *Hub) Replay(ctx context.Context, session, after string, emit func(Frame) error) error {
	index := 0
	h.mu.Lock()
	for i, f := range h.get(session).frames {
		if after != "" && f.ID == after {
			index = i + 1
			break
		}
	}
	h.mu.Unlock()
	for {
		h.mu.Lock()
		l := h.get(session)
		batch := append([]Frame(nil), l.frames[index:]...)
		index = len(l.frames)
		wake := l.wake
		h.mu.Unlock()
		for _, f := range batch {
			if err := emit(f); err != nil {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wake:
		}
	}
}

// Frames returns an immutable snapshot for reconciling a late acceptance receipt.
func (h *Hub) Frames(session string) []Frame {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Frame(nil), h.get(session).frames...)
}
