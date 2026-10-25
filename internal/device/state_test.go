package device

import "testing"

func TestStatePublishesSnapshotsAndClosesPreviousWatchers(t *testing.T) {
	state := NewState("device-a")
	initial, first := state.Watch()
	if initial.DeviceID != "device-a" || initial.Status != "ready" || initial.Revision != 0 {
		t.Fatalf("initial snapshot: %+v", initial)
	}
	state.Update(func(snapshot *Snapshot) {
		snapshot.TaskRef = "task-a"
		snapshot.Status = "running"
	})
	select {
	case <-first:
	default:
		t.Fatal("previous watcher was not notified")
	}
	updated, second := state.Watch()
	if updated.TaskRef != "task-a" || updated.Status != "running" || updated.Revision != 1 {
		t.Fatalf("updated snapshot: %+v", updated)
	}
	select {
	case <-second:
		t.Fatal("new watcher closed before the next update")
	default:
	}
	if got := state.Get(); got.Revision != 1 {
		t.Fatalf("Get returned stale snapshot: %+v", got)
	}
}
