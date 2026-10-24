package progress

import (
	"encoding/json"
	"testing"

	"github.com/wwcchh0123/hackathonGO-2026/internal/events"
)

func TestReadMessagePreservesAllTextBlocksAndSource(t *testing.T) {
	f := events.Frame{ID: "event", Type: "agent.message", Data: json.RawMessage(`{"processed_at":"2026-10-25T00:00:00Z","created_at":"2026-10-24T00:00:00Z","content":[{"type":"text","text":" "},{"type":"tool_use","text":"hidden"},{"type":"text","text":"first"},{"type":"text","text":"second"}]}`)}
	m := ReadMessage(f)
	if m.Text != "first\nsecond" || m.EventID != "event" || m.Time.Day() != 25 {
		t.Fatal(m)
	}
	f.Type = "user.message"
	f.Data = json.RawMessage(`{"created_at":"2026-10-24T00:00:00Z","input":{"parts":[{"type":"text","text":"original"}]}}`)
	m = ReadMessage(f)
	if m.Text != "original" || m.Time.Day() != 24 {
		t.Fatal(m)
	}
	for _, kind := range []string{"session.status_idle", "agent.tool_use", "session.error"} {
		f.Type = kind
		if ReadMessage(f).Text != "" {
			t.Fatal("invented user-facing message")
		}
	}
	f.Type = "agent.message"
	f.Data = []byte("invalid")
	if ReadMessage(f).Text != "" {
		t.Fatal("accepted invalid JSON")
	}
}
