package devicedisplay

import (
	"encoding/json"
	"testing"

	"github.com/wwcchh0123/hackathonGO-2026/internal/device"
)

func TestStoreResetsOnSessionChangeAndDeduplicates(t *testing.T) {
	store := NewStore()
	store.SetSnapshot(device.Snapshot{SessionID: "ses_1", Status: "running"})
	if !store.AppendEvent(Event{ID: "evt_1", Data: json.RawMessage(`{"type":"agent.message"}`)}) {
		t.Fatal("first event was rejected")
	}
	if store.AppendEvent(Event{ID: "evt_1", Data: json.RawMessage(`{"type":"agent.message"}`)}) {
		t.Fatal("duplicate event was accepted")
	}
	store.SetSnapshot(device.Snapshot{SessionID: "ses_2", Status: "ready"})
	_, events := store.Snapshot()
	if len(events) != 0 {
		t.Fatalf("old session events remain: %d", len(events))
	}
}
