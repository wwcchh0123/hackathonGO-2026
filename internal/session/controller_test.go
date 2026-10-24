package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wwcchh0123/hackathonGO-2026/internal/agentapi"
	"github.com/wwcchh0123/hackathonGO-2026/internal/artifacts"
	"github.com/wwcchh0123/hackathonGO-2026/internal/device"
	"github.com/wwcchh0123/hackathonGO-2026/internal/events"
)

type testProvider struct {
	agentapi.Provider
	create    func(context.Context, ...agentapi.NewSession) (agentapi.Session, error)
	send      func(context.Context, string, agentapi.Message) (agentapi.AcceptedInput, error)
	status    func(context.Context, string) (agentapi.Session, error)
	interrupt func(context.Context, string) error
	list      func(context.Context, agentapi.SessionQuery) (agentapi.SessionPage, error)
	eventList func(context.Context, string, agentapi.EventQuery) (agentapi.EventPage, error)
	stream    func(context.Context, string, func(events.Frame) error) error
	files     func(context.Context, string) ([]agentapi.File, error)
}

func (p *testProvider) Create(ctx context.Context, o ...agentapi.NewSession) (agentapi.Session, error) {
	if p.create != nil {
		return p.create(ctx, o...)
	}
	return p.Provider.Create(ctx, o...)
}
func (p *testProvider) SendMessage(ctx context.Context, id string, m agentapi.Message) (agentapi.AcceptedInput, error) {
	if p.send != nil {
		return p.send(ctx, id, m)
	}
	return p.Provider.SendMessage(ctx, id, m)
}
func (p *testProvider) Status(ctx context.Context, id string) (agentapi.Session, error) {
	if p.status != nil {
		return p.status(ctx, id)
	}
	return p.Provider.Status(ctx, id)
}
func (p *testProvider) Interrupt(ctx context.Context, id string) error {
	if p.interrupt != nil {
		return p.interrupt(ctx, id)
	}
	return p.Provider.Interrupt(ctx, id)
}
func (p *testProvider) ListSessions(ctx context.Context, q agentapi.SessionQuery) (agentapi.SessionPage, error) {
	if p.list != nil {
		return p.list(ctx, q)
	}
	return p.Provider.ListSessions(ctx, q)
}
func (p *testProvider) ListEvents(ctx context.Context, id string, q agentapi.EventQuery) (agentapi.EventPage, error) {
	if p.eventList != nil {
		return p.eventList(ctx, id, q)
	}
	return p.Provider.ListEvents(ctx, id, q)
}
func (p *testProvider) Stream(ctx context.Context, id string, f func(events.Frame) error) error {
	if p.stream != nil {
		return p.stream(ctx, id, f)
	}
	return p.Provider.Stream(ctx, id, f)
}
func (p *testProvider) Artifacts(ctx context.Context, id string) ([]agentapi.File, error) {
	if p.files != nil {
		return p.files(ctx, id)
	}
	return p.Provider.Artifacts(ctx, id)
}
func until(t *testing.T, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatal("condition timed out")
		}
		time.Sleep(time.Millisecond)
	}
}
func setup(t *testing.T) (*Controller, *testProvider, *agentapi.Mock) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	m := agentapi.NewMock(ctx)
	m.Step = time.Hour
	p := &testProvider{Provider: m}
	c := New(ctx, p, device.NewState("test"), &artifacts.Store{Root: t.TempDir(), PublicURL: "http://example.test"}, Options{PollInterval: time.Millisecond, RequestTimeout: time.Second, StopTimeout: time.Second})
	t.Cleanup(func() { cancel(); c.Wait() })
	return c, p, m
}
func submit(t *testing.T, c *Controller, action, text, ref, conv string) Receipt {
	t.Helper()
	r, err := c.Submit(Command{Action: action, Prompt: text, TaskRef: ref, ConversationID: conv})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func await(t *testing.T, c *Controller, r Receipt, state string) {
	t.Helper()
	until(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.tasks[r.TaskRef].commands[r.CommandID].State == state
	})
}
func detail(t *testing.T, c *Controller, ref string) Detail {
	t.Helper()
	zero := 0
	d, err := c.GetTask(context.Background(), ref, DetailOptions{EventsLimit: &zero})
	if err != nil {
		t.Fatal(err)
	}
	return d
}
func existing(t *testing.T, c *Controller, m *agentapi.Mock, title string) string {
	t.Helper()
	s, err := m.Create(context.Background(), agentapi.NewSession{Title: title})
	if err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	ref, err := c.referenceLocked(s)
	c.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func TestIndependentTasksExplicitTargetsAndConversationDefaults(t *testing.T) {
	c, p, m := setup(t)
	var interrupts atomic.Int32
	p.interrupt = func(ctx context.Context, id string) error { interrupts.Add(1); return m.Interrupt(ctx, id) }
	a := submit(t, c, "start", "A", "", "voice")
	await(t, c, a, "confirmed")
	ad := detail(t, c, a.TaskRef)
	if _, err := c.SelectView(context.Background(), a.TaskRef); err != nil {
		t.Fatal(err)
	}
	b := submit(t, c, "start", "B", "", "voice")
	await(t, c, b, "confirmed")
	if interrupts.Load() != 0 {
		t.Fatal("new task interrupted A")
	}
	if s, _ := m.Status(context.Background(), ad.SessionID); s.Status != "running" {
		t.Fatal(s)
	}
	if c.State.Get().TaskRef != a.TaskRef {
		t.Fatal("new task changed explicit display")
	}
	// Read A, then implicit send must still target B, the last write in this conversation.
	detail(t, c, a.TaskRef)
	r := submit(t, c, "message", "继续", "", "voice")
	if r.TaskRef != b.TaskRef {
		t.Fatal("query changed default send target")
	}
	await(t, c, r, "confirmed")
	if _, err := c.SelectView(context.Background(), b.TaskRef); err != nil {
		t.Fatal(err)
	}
	r = submit(t, c, "message", "修改A", a.TaskRef, "voice")
	await(t, c, r, "confirmed")
	if c.State.Get().TaskRef != b.TaskRef || detail(t, c, a.TaskRef).SessionID != ad.SessionID {
		t.Fatal("display/command target drift")
	}
	for _, conv := range []string{"", "another-conversation"} {
		if _, err := c.Submit(Command{Action: "message", Prompt: "继续", ConversationID: conv}); err == nil {
			t.Fatal("guessed target")
		}
	}
}
func TestModifyWaitsForRealIdle(t *testing.T) {
	c, p, m := setup(t)
	a := submit(t, c, "start", "A", "", "v")
	await(t, c, a, "confirmed")
	stopped, release, sent := make(chan struct{}), make(chan struct{}), make(chan struct{})
	p.interrupt = func(ctx context.Context, id string) error {
		close(stopped)
		go func() {
			select {
			case <-release:
				_ = m.Interrupt(ctx, id)
			case <-ctx.Done():
			}
		}()
		return nil
	}
	p.send = func(ctx context.Context, id string, msg agentapi.Message) (agentapi.AcceptedInput, error) {
		close(sent)
		return m.SendMessage(ctx, id, msg)
	}
	r := submit(t, c, "message", "修改", a.TaskRef, "v")
	<-stopped
	select {
	case <-sent:
		t.Fatal("send before confirmed idle")
	case <-time.After(15 * time.Millisecond):
	}
	close(release)
	await(t, c, r, "confirmed")
	if detail(t, c, r.TaskRef).SessionID != detail(t, c, a.TaskRef).SessionID {
		t.Fatal("replaced Session")
	}
}
func TestQueueDuringCreationAndStableDedup(t *testing.T) {
	c, p, m := setup(t)
	gate := make(chan struct{})
	entered := make(chan struct{})
	var creates atomic.Int32
	p.create = func(ctx context.Context, o ...agentapi.NewSession) (agentapi.Session, error) {
		if creates.Add(1) == 1 {
			close(entered)
		}
		select {
		case <-gate:
			return m.Create(ctx, o...)
		case <-ctx.Done():
			return agentapi.Session{}, ctx.Err()
		}
	}
	r := submit(t, c, "start", "one", "", "v")
	<-entered
	dup := submit(t, c, "start", "one", "", "v")
	if dup != r {
		t.Fatal("new_task duplicate allocated new reference")
	}
	pending := detail(t, c, r.TaskRef)
	if pending.SessionID != "" || pending.Command.State != "submitting" {
		t.Fatal(pending)
	}
	next := submit(t, c, "message", "two", "", "v")
	if next.TaskRef != r.TaskRef {
		t.Fatal("pending reference lost")
	}
	b := submit(t, c, "start", "other", "", "v")
	close(gate)
	await(t, c, next, "confirmed")
	await(t, c, b, "confirmed")
	if creates.Load() != 2 {
		t.Fatal(creates.Load())
	}
	// Canonical reference and text distinguish targets, and expired dedup entries can be admitted again.
	ra := submit(t, c, "message", "same", r.TaskRef, "v")
	rb := submit(t, c, "message", "same", b.TaskRef, "v")
	if ra.CommandID == rb.CommandID {
		t.Fatal("cross-task dedup")
	}
	await(t, c, rb, "confirmed")
	c.mu.Lock()
	for key, e := range c.recent {
		e.expires = time.Now().Add(-time.Second)
		c.recent[key] = e
	}
	c.mu.Unlock()
	fresh := submit(t, c, "message", "same", b.TaskRef, "v")
	if fresh.CommandID == rb.CommandID {
		t.Fatal("dedup did not expire")
	}
}
func TestUnknownSendReconcilesWithoutResending(t *testing.T) {
	c, p, m := setup(t)
	var sends atomic.Int32
	p.send = func(ctx context.Context, id string, msg agentapi.Message) (agentapi.AcceptedInput, error) {
		sends.Add(1)
		_, err := m.SendMessage(ctx, id, msg)
		if err != nil {
			return agentapi.AcceptedInput{}, err
		}
		return agentapi.AcceptedInput{}, &agentapi.Error{Operation: "POST", Ambiguous: true}
	}
	r := submit(t, c, "start", "one", "", "v")
	await(t, c, r, "confirmed")
	d := detail(t, c, r.TaskRef)
	if sends.Load() != 1 || d.Command.EventID == "" {
		t.Fatal(d.Command, sends.Load())
	}
}
func TestUnknownIsQueryableAndFencesOnlyItsTask(t *testing.T) {
	c, p, m := setup(t)
	var sends atomic.Int32
	p.send = func(ctx context.Context, id string, msg agentapi.Message) (agentapi.AcceptedInput, error) {
		sends.Add(1)
		if msg.Text == "unknown" {
			return agentapi.AcceptedInput{}, &agentapi.Error{Operation: "POST", Ambiguous: true}
		}
		return m.SendMessage(ctx, id, msg)
	}
	a := submit(t, c, "start", "unknown", "", "v")
	await(t, c, a, "unknown")
	d := detail(t, c, a.TaskRef)
	if d.Command.State != "unknown" || d.Status != "idle" {
		t.Fatal(d)
	}
	blocked := submit(t, c, "message", "cannot overtake", a.TaskRef, "v")
	await(t, c, blocked, "failed")
	b := submit(t, c, "start", "B", "", "v")
	await(t, c, b, "confirmed")
	if sends.Load() != 2 {
		t.Fatal("unknown task was resent or prevented B", sends.Load())
	}
	zero := 0
	old, err := c.GetTask(context.Background(), a.TaskRef, DetailOptions{CommandID: a.CommandID, EventsLimit: &zero})
	if err != nil || old.Command.State != "unknown" {
		t.Fatal(old, err)
	}
}
func TestCreateUnknownRecoveryAndFailureNeverRecreate(t *testing.T) {
	for _, unknown := range []bool{true, false} {
		t.Run(fmt.Sprint(unknown), func(t *testing.T) {
			c, p, m := setup(t)
			var count atomic.Int32
			p.create = func(ctx context.Context, o ...agentapi.NewSession) (agentapi.Session, error) {
				count.Add(1)
				if unknown {
					_, _ = m.Create(ctx, o...)
					return agentapi.Session{}, &agentapi.Error{Operation: "POST", Ambiguous: true}
				}
				return agentapi.Session{}, errors.New("creation rejected")
			}
			r := submit(t, c, "start", "first", "", "v")
			state := "failed"
			if unknown {
				state = "unknown"
			}
			await(t, c, r, state)
			d := detail(t, c, r.TaskRef)
			if d.Command.State != "failed" {
				t.Fatal(d)
			}
			if unknown && d.SessionID == "" {
				t.Fatal("creation not recovered")
			}
			if !unknown {
				next := submit(t, c, "message", "try", r.TaskRef, "v")
				await(t, c, next, "failed")
				if detail(t, c, r.TaskRef).SessionID != "" {
					t.Fatal("invented cloud session")
				}
			}
			if count.Load() != 1 {
				t.Fatal("automatically recreated Session")
			}
		})
	}
}
func TestCommandFailureIsSeparateFromCloudStateAndOldCommandRemainsQueryable(t *testing.T) {
	c, p, m := setup(t)
	ref := existing(t, c, m, "old task")
	p.send = func(context.Context, string, agentapi.Message) (agentapi.AcceptedInput, error) {
		return agentapi.AcceptedInput{}, &agentapi.Error{Operation: "POST", StatusCode: 403}
	}
	r := submit(t, c, "message", "implement PR", ref, "v")
	await(t, c, r, "failed")
	d := detail(t, c, ref)
	if d.Status != "idle" || d.Command.Error == "" || d.Command.Phase != "send" {
		t.Fatal(d)
	}
	p.send = nil
	next := submit(t, c, "message", "retry explicitly", ref, "v")
	await(t, c, next, "confirmed")
	zero := 0
	d, err := c.GetTask(context.Background(), ref, DetailOptions{CommandID: r.CommandID, EventsLimit: &zero})
	if err != nil || d.Command.State != "failed" {
		t.Fatal(d, err)
	}
}
func TestReferencesPermissionsAndCompatibility(t *testing.T) {
	c, p, m := setup(t)
	ref := existing(t, c, m, "historic")
	if _, err := c.GetTask(context.Background(), "unknown", DetailOptions{}); !errors.Is(err, ErrReference) {
		t.Fatal(err)
	}
	for _, kind := range []string{"terminated", "native", "archived", "forbidden"} {
		t.Run(kind, func(t *testing.T) {
			p.status = func(ctx context.Context, id string) (agentapi.Session, error) {
				s, err := m.Status(ctx, id)
				switch kind {
				case "terminated":
					s.Status = "terminated"
				case "native":
					s.Runtime = "native"
				case "archived":
					now := time.Now()
					s.ArchivedAt = &now
				case "forbidden":
					return agentapi.Session{}, &agentapi.Error{StatusCode: 404}
				}
				return s, err
			}
			r := submit(t, c, "message", kind, ref, "")
			await(t, c, r, "failed")
			if d := detailLocal(c, r); d.Error == "" {
				t.Fatal(d)
			}
		})
	}
	p.status = nil
	c.mu.Lock()
	c.tasks[ref].expires = time.Now().Add(-time.Second)
	c.mu.Unlock()
	if _, err := c.SessionFor(context.Background(), ref); !errors.Is(err, ErrReference) {
		t.Fatal(err)
	}
	page, err := c.ListTasks(context.Background(), ListOptions{})
	if err != nil || len(page.Data) != 1 || page.Data[0].Ref == ref {
		t.Fatal(page, err)
	}
}
func detailLocal(c *Controller, r Receipt) CommandStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return *c.tasks[r.TaskRef].commands[r.CommandID]
}
func TestAdmissionLimits(t *testing.T) {
	c, p, _ := setup(t)
	gate := make(chan struct{})
	entered := make(chan struct{})
	p.create = func(ctx context.Context, o ...agentapi.NewSession) (agentapi.Session, error) {
		close(entered)
		select {
		case <-gate:
			return agentapi.Session{}, errors.New("stop")
		case <-ctx.Done():
			return agentapi.Session{}, ctx.Err()
		}
	}
	for _, cmd := range []Command{{Action: "show"}, {Action: "message", Prompt: " "}, {Action: "start", Prompt: strings.Repeat("字", 2001)}, {Action: "message", Prompt: "x"}, {Action: "start", Prompt: "x", ParentRef: "bad"}} {
		if _, err := c.Submit(cmd); err == nil {
			t.Fatal(cmd)
		}
	}
	submit(t, c, "start", strings.Repeat("字", 2000), "", "")
	<-entered
	for i := range 32 {
		submit(t, c, "start", fmt.Sprint(i), "", "")
	}
	if _, err := c.Submit(Command{Action: "start", Prompt: "overflow"}); !errors.Is(err, ErrQueueFull) {
		t.Fatal(err)
	}
	// Cancel via fixture cleanup; no blocked runtime is left behind.
}

func frame(id, kind, text string) events.Frame {
	raw, _ := json.Marshal(map[string]any{"id": id, "type": kind, "processed_at": "2026-10-24T10:00:00Z", "content": []map[string]string{{"type": "text", "text": text}}, "unknown": map[string]bool{"preserved": true}})
	return events.Frame{ID: id, Type: kind, Data: raw}
}
func TestSummaryPartialListHistoryAndStopReason(t *testing.T) {
	c, p, m := setup(t)
	a := existing(t, c, m, "A")
	b := existing(t, c, m, "B")
	aid := detail(t, c, a).SessionID
	p.eventList = func(ctx context.Context, id string, q agentapi.EventQuery) (agentapi.EventPage, error) {
		if id != aid {
			return agentapi.EventPage{}, errors.New("summary unavailable")
		}
		if len(q.Types) == 1 {
			return agentapi.EventPage{Data: []events.Frame{frame("empty", "agent.message", "  "), frame("summary", "agent.message", "previous round result")}}, nil
		}
		if len(q.Types) > 1 {
			return agentapi.EventPage{Data: []events.Frame{{ID: "idle", Type: "session.status_idle", Data: json.RawMessage(`{"stop_reason":{"type":"retries_exhausted"}}`)}, frame("user", "user.message", "input")}}, nil
		}
		return agentapi.EventPage{Data: []events.Frame{frame("tail", "agent.message", "text")}, NextPage: "older"}, nil
	}
	page, err := c.ListTasks(context.Background(), ListOptions{Limit: 1})
	if err != nil || !page.HasMore || page.Sort != "created_at_desc" {
		t.Fatal(page, err)
	}
	page, err = c.ListTasks(context.Background(), ListOptions{})
	if err != nil || !page.Partial || len(page.Data) != 2 {
		t.Fatal(page, err)
	}
	d, err := c.GetTask(context.Background(), a, DetailOptions{})
	if err != nil || d.Summary.Text != "previous round result" || d.StopReason != "retries_exhausted" || !d.EventsHasMore || !strings.Contains(string(d.Events[0]), "preserved") {
		t.Fatal(d, err)
	}
	p.status = func(ctx context.Context, id string) (agentapi.Session, error) {
		s, err := m.Status(ctx, id)
		s.Status = "running"
		return s, err
	}
	d = detail(t, c, a)
	if d.StopReason != "unknown" {
		t.Fatal("old idle overrode running", d)
	}
	if b == a {
		t.Fatal("reference collision")
	}
	msg := parseMessage(events.Frame{ID: "multi", Type: "agent.message", Data: json.RawMessage(`{"content":[{"type":"text","text":""},{"type":"text","text":"first"},{"type":"text","text":"second"}]}`)})
	if msg.Text != "first\nsecond" {
		t.Fatal(msg)
	}
}
func TestNoBackgroundSubscriptionAndObservationBoundary(t *testing.T) {
	c, p, m := setup(t)
	ref := existing(t, c, m, "history")
	var calls atomic.Int32
	p.eventList = func(context.Context, string, agentapi.EventQuery) (agentapi.EventPage, error) {
		data := []events.Frame{}
		for i := 100; i >= 91; i-- {
			data = append(data, frame(fmt.Sprint(i), "agent.message", "message"))
		}
		return agentapi.EventPage{Data: data}, nil
	}
	p.stream = func(ctx context.Context, id string, emit func(events.Frame) error) error {
		n := calls.Add(1)
		end := 101
		if n > 1 {
			end = 102
		}
		for i := 1; i <= end; i++ {
			if err := emit(frame(fmt.Sprint(i), "agent.message", "message")); err != nil {
				return err
			}
		}
		return io.EOF
	}
	_, _ = c.ListTasks(context.Background(), ListOptions{})
	detail(t, c, ref)
	if calls.Load() != 0 {
		t.Fatal("query opened stream")
	}
	if _, err := c.Observe(context.Background(), ref, "evicted"); !errors.Is(err, ErrCursorExpired) {
		t.Fatal(err)
	}
	observation, err := c.Observe(context.Background(), ref, "95")
	if err != nil {
		t.Fatal(err)
	}
	stop := errors.New("done")
	got := []string{}
	err = observation.Run("95", func(f events.Frame) error {
		got = append(got, f.ID)
		if f.ID == "102" {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) || !reflect.DeepEqual(got, []string{"96", "97", "98", "99", "100", "101", "102"}) {
		t.Fatal(got, err)
	}
	if len(c.observing) != 0 {
		t.Fatal("observer leaked")
	}
	held := []*Observation{}
	for range 4 {
		o, err := c.Observe(context.Background(), ref, "")
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, o)
	}
	if _, err := c.Observe(context.Background(), ref, ""); err == nil {
		t.Fatal("unbounded observers")
	}
	for _, o := range held {
		o.Close()
	}
}
func TestPreviewIsExplicitAndCannotOverwriteOtherView(t *testing.T) {
	c, p, m := setup(t)
	a := existing(t, c, m, "A")
	b := existing(t, c, m, "B")
	gate, entered := make(chan struct{}), make(chan struct{})
	p.files = func(ctx context.Context, id string) ([]agentapi.File, error) {
		close(entered)
		select {
		case <-gate:
			return []agentapi.File{{Path: "index.html", Data: []byte("<h1>A</h1>")}}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if _, err := c.SelectView(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := c.Preview(context.Background(), a); done <- err }()
	<-entered
	if _, err := c.SelectView(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	close(gate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if c.State.Get().TaskRef != b || c.State.Get().ResultURL != "" {
		t.Fatal("late preview replaced B")
	}
	if detail(t, c, a).ResultURL == "" {
		t.Fatal("A preview not queryable")
	}
}

func TestHandoffAndRestartUseActualHistoricalSession(t *testing.T) {
	c, _, m := setup(t)
	parent := existing(t, c, m, "old task")
	original := detail(t, c, parent).SessionID
	r, err := c.Submit(Command{Action: "start", Prompt: "接力这个任务", ParentRef: parent, ConversationID: "v"})
	if err != nil {
		t.Fatal(err)
	}
	await(t, c, r, "confirmed")
	child := detail(t, c, r.TaskRef)
	s, err := m.Status(context.Background(), child.SessionID)
	if err != nil || s.Metadata["parent_session_id"] != original {
		t.Fatal(s, err)
	}
	// Another controller has no task refs or projections but the same upstream.
	ctx, cancel := context.WithCancel(context.Background())
	other := New(ctx, m, device.NewState("other"), &artifacts.Store{Root: t.TempDir()})
	defer func() { cancel(); other.Wait() }()
	if _, err := other.GetTask(ctx, parent, DetailOptions{}); !errors.Is(err, ErrReference) {
		t.Fatal("old process reference survived", err)
	}
	page, err := other.ListTasks(ctx, ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var recovered string
	for _, task := range page.Data {
		if task.SessionID == original {
			recovered = task.Ref
		}
	}
	if recovered == "" || recovered == parent {
		t.Fatal("history was not rediscovered")
	}
	zero := 0
	d, err := other.GetTask(ctx, recovered, DetailOptions{EventsLimit: &zero})
	if err != nil || d.SessionID != original {
		t.Fatal(d, err)
	}
}
func TestViewIgnoresOtherTaskEventsAndOldCommandResults(t *testing.T) {
	c, _, m := setup(t)
	a := existing(t, c, m, "A")
	b := existing(t, c, m, "B")
	if _, err := c.SelectView(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	gen := c.viewGeneration
	if _, err := c.SelectView(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	c.reduceView(context.Background(), a, gen, frame("old", "agent.message", "must not replace B"))
	if c.State.Get().TaskRef != b || c.State.Get().Summary == "must not replace B" {
		t.Fatal(c.State.Get())
	}
	r := submit(t, c, "message", "A one", a, "v")
	await(t, c, r, "confirmed")
	later := submit(t, c, "message", "A two", a, "v")
	await(t, c, later, "confirmed")
	if _, err := c.SelectView(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	c.updateCommand(a, r.CommandID, "failed", "send", errors.New("late old failure"), "")
	if c.State.Get().CommandID != later.CommandID || c.State.Get().Error != "" {
		t.Fatal("old command replaced latest view", c.State.Get())
	}
}
func TestPreviewGenerationLimitsAndEviction(t *testing.T) {
	c, p, m := setup(t)
	a := existing(t, c, m, "A")
	for range 2 {
		c.previewing <- struct{}{}
	}
	if _, err := c.Preview(context.Background(), a); err == nil {
		t.Fatal("unbounded preview concurrency")
	}
	<-c.previewing
	<-c.previewing
	gate, entered := make(chan struct{}), make(chan struct{})
	p.files = func(ctx context.Context, id string) ([]agentapi.File, error) {
		close(entered)
		select {
		case <-gate:
			return []agentapi.File{{Path: "index.html", Data: []byte("old")}}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	done := make(chan error, 1)
	go func() { _, err := c.Preview(context.Background(), a); done <- err }()
	<-entered
	c.mu.Lock()
	c.tasks[a].generation++
	c.mu.Unlock()
	close(gate)
	if err := <-done; err == nil {
		t.Fatal("published stale generation")
	}
	if detail(t, c, a).ResultURL != "" {
		t.Fatal("stale URL retained")
	}
	p.files = func(context.Context, string) ([]agentapi.File, error) {
		return []agentapi.File{{Path: "index.html", Data: []byte("new")}}, nil
	}
	url, err := c.Preview(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if !c.store.Available(url) {
		t.Fatal("valid preview unavailable")
	}
	_ = c.store.Discard(url)
	if detail(t, c, a).ResultURL != "" {
		t.Fatal("evicted preview URL still advertised")
	}
}
func TestStopReasonRequiresLatestInputAndCurrentIdle(t *testing.T) {
	idle := events.Frame{Type: "session.status_idle", Data: json.RawMessage(`{"stop_reason":{"type":"end_turn"}}`)}
	user := frame("user", "user.message", "input")
	for _, tt := range []struct {
		state string
		tail  []events.Frame
		want  string
	}{{"idle", []events.Frame{idle, user}, "end_turn"}, {"running", []events.Frame{idle, user}, "unknown"}, {"idle", []events.Frame{user, idle}, "unknown"}, {"idle", []events.Frame{idle}, "unknown"}, {"idle", []events.Frame{{Type: "session.status_idle", Data: []byte(`{}`)}, idle, user}, "unknown"}} {
		if got := stopReason(tt.state, tt.tail); got != tt.want {
			t.Fatal(got, tt.want)
		}
	}
}

func TestMultipleObserversApplyEventOnlyOnce(t *testing.T) {
	c, _, m := setup(t)
	ref := existing(t, c, m, "A")
	if _, err := c.SelectView(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	seq := func(id, kind, text string, n int) events.Frame {
		f := frame(id, kind, text)
		var raw map[string]any
		_ = json.Unmarshal(f.Data, &raw)
		raw["sequence_number"] = n
		f.Data, _ = json.Marshal(raw)
		return f
	}
	input := seq("input", "user.message", "one", 1)
	c.reduceView(context.Background(), ref, c.viewGeneration, input)
	c.reduceView(context.Background(), ref, c.viewGeneration, input)
	c.reduceView(context.Background(), ref, c.viewGeneration, seq("latest", "agent.message", "new", 3))
	c.reduceView(context.Background(), ref, c.viewGeneration, seq("old", "agent.message", "old", 2))
	c.mu.Lock()
	generation := c.tasks[ref].generation
	c.mu.Unlock()
	if generation != 1 || c.State.Get().Summary != "new" {
		t.Fatal("duplicate or stale observer applied", generation, c.State.Get())
	}
}
func TestSelectedPendingTaskPublishesBindingAndObservationStaysAcrossIdle(t *testing.T) {
	c, p, m := setup(t)
	m.Step = time.Millisecond
	gate, entered := make(chan struct{}), make(chan struct{})
	p.create = func(ctx context.Context, o ...agentapi.NewSession) (agentapi.Session, error) {
		close(entered)
		select {
		case <-gate:
			return m.Create(ctx, o...)
		case <-ctx.Done():
			return agentapi.Session{}, ctx.Err()
		}
	}
	r := submit(t, c, "start", "A", "", "v")
	<-entered
	view, err := c.SelectView(context.Background(), r.TaskRef)
	if err != nil || view.SessionID != "" {
		t.Fatal(view, err)
	}
	close(gate)
	await(t, c, r, "confirmed")
	if c.State.Get().SessionID == "" {
		t.Fatal("selected pending binding never published")
	}
	until(t, func() bool {
		s, _ := m.Status(context.Background(), c.State.Get().SessionID)
		return s.Status == "idle"
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	obs, err := c.Observe(ctx, r.TaskRef, "")
	if err != nil {
		t.Fatal(err)
	}
	if c.State.Get().Status != "idle" || c.State.Get().StopReason != "end_turn" {
		t.Fatal("observation did not hydrate current state", c.State.Get())
	}
	stop := errors.New("done")
	idle := make(chan struct{}, 3)
	done := make(chan error, 1)
	go func() {
		n := 0
		done <- obs.Run("", func(f events.Frame) error {
			if f.Type == "session.status_idle" {
				n++
				idle <- struct{}{}
				if n == 3 {
					return stop
				}
			}
			return nil
		})
	}()
	<-idle
	if _, err := m.SendMessage(ctx, c.State.Get().SessionID, agentapi.Message{Text: "second"}); err != nil {
		t.Fatal(err)
	}
	<-idle
	if _, err := m.SendMessage(ctx, c.State.Get().SessionID, agentapi.Message{Text: "third"}); err != nil {
		t.Fatal(err)
	}
	<-idle
	if err := <-done; !errors.Is(err, stop) {
		t.Fatal(err)
	}
	if len(c.observing) != 0 {
		t.Fatal("observer not released")
	}
}
