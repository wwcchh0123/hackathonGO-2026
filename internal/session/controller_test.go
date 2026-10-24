package session

import (
	"context"
	"testing"
	"time"

	"github.com/wwcchh0123/hackathonGO-2026/internal/agentapi"
	"github.com/wwcchh0123/hackathonGO-2026/internal/artifacts"
	"github.com/wwcchh0123/hackathonGO-2026/internal/device"
)

type delayedStop struct {
	agentapi.Provider
	interrupted, release, sent chan struct{}
}

func (p *delayedStop) Interrupt(ctx context.Context, id string) error {
	close(p.interrupted)
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-p.release:
			_ = p.Provider.Interrupt(ctx, id)
		}
	}()
	return nil // Cloud acknowledged, but its runtime is still running.
}
func (p *delayedStop) Send(ctx context.Context, id, prompt string) error {
	if prompt == "按钮改红" {
		close(p.sent)
	}
	return p.Provider.Send(ctx, id, prompt)
}
func until(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.After(4 * time.Second)
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		if condition() {
			return
		}
		select {
		case <-deadline:
			t.Fatal("condition timed out")
		case <-tick.C:
		}
	}
}
func TestModifyWaitsForRuntimeIdleAndReusesSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	mock := agentapi.NewMock(ctx)
	mock.Step = time.Second
	p := &delayedStop{Provider: mock, interrupted: make(chan struct{}), release: make(chan struct{}), sent: make(chan struct{})}
	c := New(ctx, p, device.NewState("test"), &artifacts.Store{Root: t.TempDir(), PublicURL: "http://example.test"})
	defer func() { cancel(); c.Wait() }()
	if _, err := c.Submit(Command{Action: "start", Prompt: "做个页面"}); err != nil {
		t.Fatal(err)
	}
	until(t, func() bool { return c.State.Get().Status == "running" })
	id := c.State.Get().SessionID
	if _, err := c.Submit(Command{Action: "interrupt", Prompt: "按钮改红"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.interrupted:
	case <-time.After(time.Second):
		t.Fatal("interrupt never sent")
	}
	select {
	case <-p.sent:
		t.Fatal("sent new input before runtime stopped")
	case <-time.After(30 * time.Millisecond):
	}
	close(p.release)
	select {
	case <-p.sent:
	case <-time.After(time.Second):
		t.Fatal("new input not sent after idle")
	}
	if c.State.Get().SessionID != id {
		t.Fatal("modification replaced the session")
	}
}
func TestNewTaskReplacesSessionAndContinueKeepsIt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	mock := agentapi.NewMock(ctx)
	mock.Step = 5 * time.Millisecond
	c := New(ctx, mock, device.NewState("test"), &artifacts.Store{Root: t.TempDir(), PublicURL: "http://example.test"})
	defer func() { cancel(); c.Wait() }()
	_, err := c.Submit(Command{Action: "start", Prompt: "first"})
	if err != nil {
		t.Fatal(err)
	}
	until(t, func() bool { return c.State.Get().ResultURL != "" })
	first := c.State.Get().SessionID
	if _, err = c.Submit(Command{Action: "continue"}); err != nil {
		t.Fatal(err)
	}
	until(t, func() bool { return c.State.Get().Prompt == "继续" && c.State.Get().ResultURL != "" })
	if c.State.Get().SessionID != first {
		t.Fatal("continue replaced session")
	}
	if _, err = c.Submit(Command{Action: "start", Prompt: "second"}); err != nil {
		t.Fatal(err)
	}
	until(t, func() bool {
		return c.State.Get().SessionID != "" && c.State.Get().SessionID != first && c.State.Get().ResultURL != ""
	})
}
