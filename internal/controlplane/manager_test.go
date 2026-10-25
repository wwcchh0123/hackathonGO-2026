package controlplane

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/wwcchh0123/hackathonGO-2026/internal/agentapi"
	"github.com/wwcchh0123/hackathonGO-2026/internal/session"
)

func waitCommand(t *testing.T, c *session.Controller, receipt session.Receipt) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		zero := 0
		detail, err := c.GetTask(context.Background(), receipt.TaskRef, session.DetailOptions{CommandID: receipt.CommandID, EventsLimit: &zero})
		if err != nil {
			t.Fatal(err)
		}
		if detail.Command != nil && detail.Command.State == "confirmed" {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("command did not confirm")
}

func TestManagerRoutesDevicesAndAggregatesOwnedTasks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	mock := agentapi.NewMock(ctx)
	mock.Step = time.Millisecond
	m := New(ctx, mock, "a", t.TempDir(), "http://example.test", Options{OnlineWindow: time.Second})
	defer func() { cancel(); m.Wait() }()
	if err := m.Touch("a", "test"); err != nil {
		t.Fatal(err)
	}
	if err := m.Touch("b", "test"); err != nil {
		t.Fatal(err)
	}
	ca := m.DefaultController()
	cb, ok := m.Controller("b")
	if !ok || cb == ca {
		t.Fatal("devices did not get independent controllers")
	}
	ra, err := ca.Submit(session.Command{Action: "start", Prompt: "A"})
	if err != nil {
		t.Fatal(err)
	}
	rb, err := cb.Submit(session.Command{Action: "start", Prompt: "B"})
	if err != nil {
		t.Fatal(err)
	}
	waitCommand(t, ca, ra)
	waitCommand(t, cb, rb)
	devices, queried := m.ListDevices(ctx)
	if queried.IsZero() || len(devices) != 2 {
		t.Fatal(devices, queried)
	}
	for _, d := range devices {
		if d.TaskCounts.Total != 1 || d.TasksError != "" || d.LastSeen == nil || !d.Online {
			t.Fatalf("unexpected overview: %+v", d)
		}
	}
	if _, err := m.Snapshot("b"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ListTasks(ctx, "unknown", session.ListOptions{}); !errors.Is(err, ErrUnknownDevice) {
		t.Fatal(err)
	}
	if _, err := m.GetTask(ctx, "b", ra.TaskRef, session.DetailOptions{EventsLimit: ptr(0)}); !errors.Is(err, session.ErrReference) {
		t.Fatal("cross-device task reference was accepted", err)
	}
	if err := m.Touch("bad/id", "test"); !errors.Is(err, ErrUnknownDevice) {
		t.Fatal("invalid device id was accepted", err)
	}
}

func TestDefaultDeviceIncludesLegacySessionsOnlyForDefault(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	mock := agentapi.NewMock(ctx)
	m := New(ctx, mock, "default", t.TempDir(), "http://example.test")
	defer func() { cancel(); m.Wait() }()
	if _, err := mock.Create(ctx, agentapi.NewSession{Title: "legacy"}); err != nil {
		t.Fatal(err)
	}
	if err := m.Touch("other", "test"); err != nil {
		t.Fatal(err)
	}
	defaultTasks, err := m.ListTasks(ctx, "default", session.ListOptions{})
	if err != nil || len(defaultTasks.Data) != 1 {
		t.Fatal(defaultTasks, err)
	}
	otherTasks, err := m.ListTasks(ctx, "other", session.ListOptions{})
	if err != nil || len(otherTasks.Data) != 0 {
		t.Fatal(otherTasks, err)
	}
}

func ptr(v int) *int { return &v }
