package session

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/wwcchh0123/hackathonGO-2026/internal/agentapi"
	"github.com/wwcchh0123/hackathonGO-2026/internal/device"
	"github.com/wwcchh0123/hackathonGO-2026/internal/events"
)

var ErrCursorExpired = errors.New("事件游标已离开有界窗口，请重新读取最近事件")

type Observation struct {
	c              *Controller
	ref, id        string
	initial        []events.Frame
	ctx            context.Context
	cancel         context.CancelFunc
	stopService    func() bool
	once           sync.Once
	viewGeneration uint64
}

// Observe prepares a finite tail before opening SSE. Its boundary is also the
// checkpoint used to discard the upstream's full replay on every reconnect.
func (c *Controller) Observe(ctx context.Context, ref, after string) (*Observation, error) {
	s, err := c.SessionFor(ctx, ref)
	if err != nil {
		return nil, err
	}
	select {
	case c.observing <- struct{}{}:
	default:
		return nil, errors.New("实时观察连接已达上限")
	}
	queryCtx, done := context.WithTimeout(ctx, c.options.RequestTimeout)
	page, err := c.Provider.ListEvents(queryCtx, s.ID, agentapi.EventQuery{Limit: 100, Order: "desc"})
	if err == nil {
		s, err = c.status(queryCtx, s.ID)
	}
	done()
	if err != nil {
		<-c.observing
		return nil, err
	}
	initial := make([]events.Frame, 0, len(page.Data))
	bytes := 0
	for i := len(page.Data) - 1; i >= 0; i-- {
		bytes += len(page.Data[i].Data)
		if bytes > 8<<20 {
			<-c.observing
			return nil, errors.New("事件窗口超过 8 MiB")
		}
		initial = append(initial, page.Data[i])
	}
	if after != "" {
		found := false
		for _, f := range initial {
			if f.ID == after {
				found = true
				break
			}
		}
		if !found {
			<-c.observing
			return nil, ErrCursorExpired
		}
	}
	streamCtx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	generation := c.viewGeneration
	t, refErr := c.recordLocked(ref)
	if refErr != nil {
		c.mu.Unlock()
		cancel()
		<-c.observing
		return nil, refErr
	}
	t.observers++
	if c.State.Get().TaskRef == ref {
		c.State.Update(func(v *device.Snapshot) {
			v.SessionID = s.ID
			v.Status = s.Status
			v.StopReason = stopReason(s.Status, page.Data)
			for _, f := range page.Data {
				if f.Type == "agent.message" {
					msg := parseMessage(f)
					if msg.Text != "" {
						v.Summary = truncate(msg.Text, 400)
						v.SummaryEventID = msg.EventID
						v.SummaryTime = &msg.Time
						break
					}
				}
			}
		})
	}
	c.mu.Unlock()
	o := &Observation{c: c, ref: ref, id: s.ID, initial: initial, ctx: streamCtx, cancel: cancel, viewGeneration: generation}
	o.stopService = context.AfterFunc(c.ctx, cancel)
	return o, nil
}
func (o *Observation) Close() {
	o.once.Do(func() {
		o.cancel()
		o.stopService()
		o.c.mu.Lock()
		if t := o.c.tasks[o.ref]; t != nil {
			t.observers--
			t.expires = time.Now().Add(o.c.options.ReferenceTTL)
		}
		o.c.mu.Unlock()
		<-o.c.observing
	})
}
func (o *Observation) Run(after string, emit func(events.Frame) error) error {
	defer o.Close()
	checkpoint := ""
	seen := map[string]bool{}
	ids := []string{}
	var sequence int64
	remember := func(f events.Frame) {
		var meta struct {
			Sequence int64 `json:"sequence_number"`
		}
		_ = json.Unmarshal(f.Data, &meta)
		if meta.Sequence > sequence {
			sequence = meta.Sequence
		}
		if !seen[f.ID] {
			seen[f.ID] = true
			ids = append(ids, f.ID)
			if len(ids) > 512 {
				delete(seen, ids[0])
				ids = ids[1:]
			}
		}
	}
	past := after == ""
	for _, f := range o.initial {
		checkpoint = f.ID
		remember(f)
		if !past {
			if f.ID == after {
				past = true
			}
			continue
		}
		if err := emit(f); err != nil {
			return err
		}
	}
	o.initial = nil
	delay := 100 * time.Millisecond
	for o.ctx.Err() == nil {
		replaying := checkpoint != ""
		streamCtx, cancel := context.WithCancel(o.ctx)
		// A stream that cannot catch its boundary must not hang as a fake live view.
		timer := time.AfterFunc(o.c.options.RequestTimeout, func() { cancel() })
		if !replaying {
			timer.Stop()
		}
		var consumerErr error
		err := o.c.Provider.Stream(streamCtx, o.id, func(f events.Frame) error {
			if f.ID == "" || strings.ContainsAny(f.ID, "\r\n") || len(f.Data) > 8<<20 {
				return errors.New("无效或过大的上游事件")
			}
			if replaying {
				if f.ID == checkpoint {
					replaying = false
					timer.Stop()
				}
				return nil
			}
			var meta struct {
				Sequence int64 `json:"sequence_number"`
			}
			_ = json.Unmarshal(f.Data, &meta)
			if seen[f.ID] || (meta.Sequence > 0 && meta.Sequence <= sequence) {
				return nil
			}
			if err := emit(f); err != nil {
				consumerErr = err
				return err
			}
			checkpoint = f.ID
			remember(f)
			o.c.reduceView(streamCtx, o.ref, o.viewGeneration, f)
			return nil
		})
		timer.Stop()
		cancel()
		if consumerErr != nil {
			return consumerErr
		}
		if o.ctx.Err() != nil {
			return o.ctx.Err()
		}
		if agentapi.IsStatus(err, 401) || agentapi.IsStatus(err, 403) || agentapi.IsStatus(err, 404) {
			return err
		}
		o.c.mu.Lock()
		if o.c.State.Get().TaskRef == o.ref && o.c.viewGeneration == o.viewGeneration {
			o.c.State.Update(func(s *device.Snapshot) { s.StreamError = "事件连接恢复中，正在追赶历史边界" })
		}
		o.c.mu.Unlock()
		t := time.NewTimer(delay)
		select {
		case <-o.ctx.Done():
			t.Stop()
			return o.ctx.Err()
		case <-t.C:
		}
		delay = min(time.Second, delay*2)
	}
	return o.ctx.Err()
}
func (c *Controller) reduceView(ctx context.Context, ref string, generation uint64, f events.Frame) {
	var meta struct {
		Sequence int64 `json:"sequence_number"`
	}
	_ = json.Unmarshal(f.Data, &meta)
	var status string
	reason := "unknown"
	if f.Type == "session.status_idle" || f.Type == "session.status_running" || f.Type == "session.status_rescheduled" || f.Type == "session.status_terminated" {
		s, err := c.SessionFor(ctx, ref)
		if err != nil {
			return
		}
		status = s.Status
		if status == "idle" {
			queryCtx, cancel := context.WithTimeout(ctx, c.options.RequestTimeout)
			tail, err := c.Provider.ListEvents(queryCtx, s.ID, agentapi.EventQuery{Limit: 64, Order: "desc", Types: []string{"user.message", "session.status_idle"}})
			cancel()
			if err == nil {
				reason = stopReason(status, tail.Data)
			}
		}
	}
	msg := Message{}
	if f.Type == "agent.message" {
		msg = parseMessage(f)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.State.Get().TaskRef != ref || c.viewGeneration != generation {
		return
	}
	t := c.tasks[ref]
	if t == nil {
		return
	}
	if t.observedIDs[f.ID] || (meta.Sequence > 0 && meta.Sequence <= t.observedSequence) {
		return
	}
	if t.observedIDs == nil {
		t.observedIDs = map[string]bool{}
	}
	t.observedIDs[f.ID] = true
	t.observedOrder = append(t.observedOrder, f.ID)
	if len(t.observedOrder) > 512 {
		delete(t.observedIDs, t.observedOrder[0])
		t.observedOrder = t.observedOrder[1:]
	}
	if meta.Sequence > t.observedSequence {
		t.observedSequence = meta.Sequence
	}
	c.State.Update(func(s *device.Snapshot) {
		s.StreamError = ""
		if status != "" {
			s.Status = status
			s.StopReason = reason
		}
		if msg.Text != "" {
			s.Summary = truncate(msg.Text, 400)
			s.SummaryEventID = msg.EventID
			s.SummaryTime = &msg.Time
		}
		if f.Type == "user.message" {
			s.StopReason = "unknown"
			s.ResultURL = ""
			if t := c.tasks[ref]; t != nil {
				t.generation++
				t.resultURL = ""
			}
		}
	})
}
func (c *Controller) SelectView(ctx context.Context, ref string) (device.Snapshot, error) {
	zero := 0
	d, err := c.GetTask(ctx, ref, DetailOptions{EventsLimit: &zero})
	if err != nil {
		return device.Snapshot{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.viewGeneration++
	c.State.Update(func(s *device.Snapshot) {
		s.TaskRef = ref
		s.SessionID = d.SessionID
		s.Status = d.Status
		s.Summary = ""
		s.SummaryEventID = ""
		s.SummaryTime = nil
		s.StopReason = d.StopReason
		if d.Summary != nil {
			s.Summary = d.Summary.Text
			s.SummaryEventID = d.Summary.EventID
			s.SummaryTime = &d.Summary.Time
		}
		s.Prompt = ""
		s.Error = ""
		if t := c.tasks[ref]; t != nil {
			s.Prompt = t.prompt
		}
		if d.Command != nil {
			s.Error = d.Command.Error
			s.CommandID = d.Command.ID
			s.CommandState = d.Command.State
		} else {
			s.CommandID = ""
			s.CommandState = ""
		}
		s.ResultURL = d.ResultURL
		s.StreamError = ""
		s.PreviewError = ""
	})
	return c.State.Get(), nil
}
