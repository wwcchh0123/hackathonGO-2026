package events

import (
	"context"
	"encoding/json"
	"errors"
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
