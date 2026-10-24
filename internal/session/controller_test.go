package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wwcchh0123/hackathonGO-2026/internal/agentapi"
	"github.com/wwcchh0123/hackathonGO-2026/internal/artifacts"
	"github.com/wwcchh0123/hackathonGO-2026/internal/device"
	"github.com/wwcchh0123/hackathonGO-2026/internal/events"
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
	if _, err := c.Submit(Command{Action: "message", Prompt: "按钮改红"}); err != nil {
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
	if _, err = c.Submit(Command{Action: "message", Prompt: "继续"}); err != nil {
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

type call struct{ action, id, text string }
type controlledProvider struct {
	mu                    sync.Mutex
	calls                 []call
	statuses              map[string]string
	createGate, stopGate  <-chan struct{}
	creating, interrupted chan struct{}
	failCreate, failSend  bool
	frames                chan events.Frame
}

func newControlled() *controlledProvider {
	return &controlledProvider{statuses: map[string]string{}, creating: make(chan struct{}, 10), interrupted: make(chan struct{}, 10), frames: make(chan events.Frame, 10)}
}
func (p *controlledProvider) Create(ctx context.Context) (agentapi.Session, error) {
	select {
	case p.creating <- struct{}{}:
	case <-ctx.Done():
		return agentapi.Session{}, ctx.Err()
	}
	if p.createGate != nil {
		select {
		case <-p.createGate:
		case <-ctx.Done():
			return agentapi.Session{}, ctx.Err()
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failCreate {
		p.failCreate = false
		return agentapi.Session{}, errors.New("create failed")
	}
	id := fmt.Sprintf("session-%d", len(p.statuses)+1)
	p.statuses[id] = "idle"
	p.calls = append(p.calls, call{action: "create", id: id})
	return agentapi.Session{ID: id, Status: "idle"}, nil
}
func (p *controlledProvider) Send(ctx context.Context, id, text string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.statuses[id] != "idle" {
		return errors.New("send before idle")
	}
	if p.failSend {
		p.failSend = false
		return errors.New("send failed")
	}
	p.calls = append(p.calls, call{"send", id, text})
	p.statuses[id] = "running"
	return nil
}
func (p *controlledProvider) Interrupt(ctx context.Context, id string) error {
	p.mu.Lock()
	p.calls = append(p.calls, call{action: "interrupt", id: id})
	p.mu.Unlock()
	select {
	case p.interrupted <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	if p.stopGate != nil {
		go func() {
			select {
			case <-p.stopGate:
				p.setStatus(id, "idle")
			case <-ctx.Done():
			}
		}()
	} else {
		p.setStatus(id, "idle")
	}
	return nil
}
func (p *controlledProvider) Status(ctx context.Context, id string) (agentapi.Session, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return agentapi.Session{ID: id, Status: p.statuses[id]}, nil
}
func (p *controlledProvider) Stream(ctx context.Context, id string, emit func(events.Frame) error) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case f := <-p.frames:
			if err := emit(f); err != nil {
				return err
			}
		}
	}
}
func (p *controlledProvider) Artifacts(context.Context, string) ([]agentapi.File, error) {
	return nil, nil
}
func (p *controlledProvider) setStatus(id, status string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.statuses[id] = status
}
func (p *controlledProvider) trace() []call {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]call(nil), p.calls...)
}
func controlled(t *testing.T, p *controlledProvider) *Controller {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	c := New(ctx, p, device.NewState("test"), &artifacts.Store{Root: t.TempDir()})
	t.Cleanup(func() { cancel(); c.Wait() })
	return c
}
func submit(t *testing.T, c *Controller, action, text string) Receipt {
	t.Helper()
	started := time.Now()
	receipt, err := c.Submit(Command{Action: action, Prompt: text})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(started) >= 300*time.Millisecond {
		t.Fatal("queue admission blocked on provider")
	}
	return receipt
}
func drained(c *Controller) bool { c.submitMu.Lock(); defer c.submitMu.Unlock(); return c.pending == 0 }

func TestMessageDecidesFromProviderAtExecutionTime(t *testing.T) {
	p := newControlled()
	c := controlled(t, p)
	submit(t, c, "message", "first") // No session creates one.
	until(t, func() bool { return drained(c) })
	first := c.State.Get().SessionID
	p.setStatus(first, "idle") // Snapshot still says running.
	submit(t, c, "message", "继续")
	until(t, func() bool { return drained(c) })
	if c.State.Get().SessionID != first {
		t.Fatal("idle session not reused")
	}
	submit(t, c, "message", "停一下") // Running: interrupt -> idle -> send.
	until(t, func() bool { return drained(c) })
	p.setStatus(first, "terminated") // Snapshot still says running.
	submit(t, c, "message", "next")
	until(t, func() bool { return drained(c) })
	trace := p.trace()
	want := []call{{"create", first, ""}, {"send", first, "first"}, {"send", first, "继续"}, {"interrupt", first, ""}, {"send", first, "停一下"}, {"create", "session-2", ""}, {"send", "session-2", "next"}}
	if !reflect.DeepEqual(trace, want) {
		t.Fatalf("trace:\n%+v\nwant:\n%+v", trace, want)
	}
}
func TestMessagesAndNewProjectsQueueDuringCreate(t *testing.T) {
	gate := make(chan struct{})
	p := newControlled()
	p.createGate = gate
	c := controlled(t, p)
	submit(t, c, "message", "first")
	<-p.creating
	receipt := submit(t, c, "message", "modify")
	if receipt.Message != "好的，先停下当前的再按你说的做" {
		t.Fatal(receipt)
	}
	submit(t, c, "start", "second")
	submit(t, c, "start", "third")
	close(gate)
	until(t, func() bool { return drained(c) })
	trace := p.trace()
	want := []call{{"create", "session-1", ""}, {"send", "session-1", "first"}, {"interrupt", "session-1", ""}, {"send", "session-1", "modify"}, {"interrupt", "session-1", ""}, {"create", "session-2", ""}, {"send", "session-2", "second"}, {"interrupt", "session-2", ""}, {"create", "session-3", ""}, {"send", "session-3", "third"}}
	if !reflect.DeepEqual(trace, want) {
		t.Fatalf("queue order: %+v", trace)
	}
}
func TestNewProjectWaitsForIdleDespiteOldIdleEvent(t *testing.T) {
	gate := make(chan struct{})
	p := newControlled()
	p.stopGate = gate
	c := controlled(t, p)
	submit(t, c, "message", "first")
	until(t, func() bool { return drained(c) })
	old := c.State.Get().SessionID
	submit(t, c, "start", "new project")
	<-p.interrupted
	// Replayed idle must never release the worker waiting on runtime status.
	p.frames <- events.Frame{ID: "old-idle", Type: "session.status_idle", Data: json.RawMessage(`{"id":"old-idle","type":"session.status_idle","stop_reason":{"type":"interrupted"}}`)}
	until(t, func() bool { return len(p.trace()) == 3 })
	if len(p.trace()) != 3 {
		t.Fatalf("new session created before runtime idle: %+v", p.trace())
	}
	close(gate)
	until(t, func() bool { return drained(c) })
	if c.State.Get().SessionID == old {
		t.Fatal("new project reused session")
	}
}
func TestFailuresRecoverAndRemainVisible(t *testing.T) {
	p := newControlled()
	p.failCreate = true
	c := controlled(t, p)
	submit(t, c, "message", "first")
	until(t, func() bool { return drained(c) })
	if c.State.Get().Status != "error" || c.State.Get().Error == "" || !strings.Contains(c.StatusText(), "操作未完成") {
		t.Fatal(c.State.Get())
	}
	submit(t, c, "message", "retry")
	until(t, func() bool { return drained(c) })
	first := c.State.Get().SessionID
	p.setStatus(first, "idle")
	p.mu.Lock()
	p.failSend = true
	p.mu.Unlock()
	submit(t, c, "message", "modify")
	until(t, func() bool { return drained(c) })
	if c.State.Get().Error == "" {
		t.Fatal("send failure not visible")
	}
	submit(t, c, "message", "retry send")
	until(t, func() bool { return drained(c) })
	if c.State.Get().SessionID != first || c.State.Get().Error != "" {
		t.Fatal(c.State.Get())
	}
}
func TestAdmissionValidationAndQueueCapacity(t *testing.T) {
	gate := make(chan struct{})
	p := newControlled()
	p.createGate = gate
	c := controlled(t, p)
	for _, cmd := range []Command{{Action: "show"}, {Action: "interrupt"}, {Action: "continue"}, {Action: "message", Prompt: " \n "}, {Action: "start", Prompt: strings.Repeat("字", 2001)}} {
		if _, err := c.Submit(cmd); err == nil {
			t.Fatalf("accepted %+v", cmd)
		}
	}
	submit(t, c, "message", strings.Repeat("字", 2000))
	<-p.creating
	for range 32 {
		submit(t, c, "message", "queued")
	}
	if _, err := c.Submit(Command{Action: "message", Prompt: "overflow"}); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("overload: %v", err)
	}
	close(gate)
}
