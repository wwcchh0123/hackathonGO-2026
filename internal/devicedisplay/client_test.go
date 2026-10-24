package devicedisplay

import (
	"strings"
	"testing"
)

func TestConsumeSSEKeepsRawJSONAndFields(t *testing.T) {
	var got []sseFrame
	err := consumeSSE(strings.NewReader("id: evt_1\nevent: agent_event\ndata: {\"text\":\"line\\nnext\"}\n\n: keep-alive\n\nid: evt_2\nevent: agent_event\ndata: {\"ok\":true}\n\n"), func(frame sseFrame) error {
		got = append(got, frame)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "evt_1" || got[0].Event != "agent_event" || got[0].Data != `{"text":"line\nnext"}` {
		t.Fatalf("unexpected first frame: %#v", got)
	}
	if got[1].ID != "evt_2" || got[1].Data != `{"ok":true}` {
		t.Fatalf("unexpected second frame: %#v", got)
	}
}
