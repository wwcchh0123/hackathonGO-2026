// Package events stores raw provider events for gap-free history + live delivery.
package events

import (
	"context"
	"encoding/json"
	"errors"
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
	base   int
	bytes  int
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
		if len(h.logs) >= 256 {
			for key, old := range h.logs {
				close(old.wake)
				delete(h.logs, key)
				break
			}
		}
		l = &log{seen: make(map[string]bool), wake: make(chan struct{})}
		h.logs[id] = l
	}
	return l
}
func (h *Hub) Append(session string, f Frame) bool {
	if len(f.Data) > 8<<20 {
		return false
	}
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
	l.bytes += len(f.Data)
	for len(l.frames) > 512 || (l.bytes > 8<<20 && len(l.frames) > 1) {
		old := l.frames[0]
		delete(l.seen, old.ID)
		l.bytes -= len(old.Data)
		l.frames[0] = Frame{}
		l.frames = l.frames[1:]
		l.base++
	}
	close(l.wake)
	l.wake = make(chan struct{})
	return true
}

var ErrCursorExpired = errors.New("event cursor expired; reload the bounded history")

// Replay starts after a known ID; evicted cursors require explicit resync.
func (h *Hub) Replay(ctx context.Context, session, after string, emit func(Frame) error) error {
	index := 0
	h.mu.Lock()
	l := h.get(session)
	index = l.base
	found := after == ""
	for i, f := range l.frames {
		if after != "" && f.ID == after {
			index = l.base + i + 1
			found = true
			break
		}
	}
	h.mu.Unlock()
	if !found {
		return ErrCursorExpired
	}
	for {
		h.mu.Lock()
		l := h.get(session)
		if index < l.base || index > l.base+len(l.frames) {
			h.mu.Unlock()
			return ErrCursorExpired
		}
		batch := append([]Frame(nil), l.frames[index-l.base:]...)
		index = l.base + len(l.frames)
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
