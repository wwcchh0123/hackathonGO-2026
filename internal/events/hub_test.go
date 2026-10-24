package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func TestHistoryLiveBoundaryAndCursor(t *testing.T) {
	h := NewHub()
	frame := func(id string) Frame {
		return Frame{ID: id, Type: "agent.message", Data: json.RawMessage(`{"id":"` + id + `","type":"agent.message"}`)}
	}
	h.Append("s", frame("1"))
	h.Append("s", frame("2"))
	if h.Append("s", frame("2")) {
		t.Fatal("replayed duplicate appended")
	}
	stop := errors.New("done")
	var got []string
	err := h.Replay(context.Background(), "s", "1", func(f Frame) error {
		got = append(got, f.ID)
		if f.ID == "2" {
			h.Append("s", frame("3"))
			return nil
		}
		return stop
	})
	if !errors.Is(err, stop) || !reflect.DeepEqual(got, []string{"2", "3"}) {
		t.Fatalf("cursor/live gap: got %v, err %v", got, err)
	}
}

func TestBoundedHistoryRejectsExpiredCursorAndSlowReader(t *testing.T) {
	h := NewHub()
	for i := range 513 {
		h.Append("s", Frame{ID: fmt.Sprint(i), Data: json.RawMessage(`{"ok":true}`)})
	}
	if len(h.Frames("s")) != 512 {
		t.Fatal("history not bounded")
	}
	if err := h.Replay(context.Background(), "s", "0", func(Frame) error { return nil }); !errors.Is(err, ErrCursorExpired) {
		t.Fatal(err)
	}
	if err := h.Replay(context.Background(), "s", "unknown", func(Frame) error { return nil }); !errors.Is(err, ErrCursorExpired) {
		t.Fatal(err)
	}
	if h.Append("s", Frame{ID: "oversized", Data: make([]byte, (8<<20)+1)}) {
		t.Fatal("oversized frame accepted")
	}
	for i := range 257 {
		h.Append(fmt.Sprint(i), Frame{ID: "one"})
	}
	if len(h.logs) > 256 {
		t.Fatal("unbounded session logs")
	}
}
