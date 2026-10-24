// Package session owns all programming decisions and serializes control commands.
package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/wwcchh0123/hackathonGO-2026/internal/agentapi"
	"github.com/wwcchh0123/hackathonGO-2026/internal/artifacts"
	"github.com/wwcchh0123/hackathonGO-2026/internal/device"
	"github.com/wwcchh0123/hackathonGO-2026/internal/events"
	"github.com/wwcchh0123/hackathonGO-2026/internal/progress"
)

var ErrQueueFull = errors.New("操作太频繁，请稍后再试")

type Command struct {
	Action         string `json:"action"`
	Prompt         string `json:"prompt,omitempty"`
	ID             string `json:"-"`
	Source         string `json:"-"`
	ConversationID string `json:"-"`
}
type Receipt struct {
	Accepted bool   `json:"accepted"`
	Message  string `json:"message"`
}

// Options supplies stage budgets. A zero field selects its default.
type Options struct{ CreateTimeout, RequestTimeout, StopTimeout, ReconcileTimeout, PublishTimeout, PollInterval time.Duration }

func defaults(o Options) Options {
	values := []*time.Duration{&o.CreateTimeout, &o.RequestTimeout, &o.StopTimeout, &o.ReconcileTimeout, &o.PublishTimeout, &o.PollInterval}
	for i, d := range []time.Duration{30 * time.Second, 10 * time.Second, 60 * time.Second, 10 * time.Second, 30 * time.Second, 500 * time.Millisecond} {
		if *values[i] <= 0 {
			*values[i] = d
		}
	}
	return o
}

type turn struct {
	command, event, terminal string
	seen, uncertain, stopped bool
}
type Controller struct {
	Provider                          agentapi.Provider
	State                             *device.State
	Events                            *events.Hub
	store                             *artifacts.Store
	ctx                               context.Context
	queue                             chan Command
	wg                                sync.WaitGroup
	options                           Options
	streamCancel                      context.CancelFunc // owned by the command worker
	submitMu                          sync.Mutex
	pending                           int
	mu                                sync.Mutex // guards generations, turn, publication, and conditional snapshot writes
	sessionGeneration, turnGeneration uint64
	current                           turn
	publishCancel                     context.CancelFunc
	published                         map[string]string // session -> content fingerprint of the last published output
	createUncertain                   bool
}

func New(ctx context.Context, p agentapi.Provider, state *device.State, store *artifacts.Store, options ...Options) *Controller {
	var o Options
	if len(options) > 0 {
		o = options[0]
	}
	c := &Controller{Provider: p, State: state, Events: events.NewHub(), store: store, ctx: ctx, queue: make(chan Command, 32), options: defaults(o), published: make(map[string]string)}
	c.wg.Add(1)
	go c.work()
	return c
}
func (c *Controller) Wait() { c.wg.Wait() }
func (c *Controller) Submit(cmd Command) (Receipt, error) {
	cmd.Prompt = strings.TrimSpace(cmd.Prompt)
	if cmd.Action != "start" && cmd.Action != "message" {
		return Receipt{}, errors.New("未知指令")
	}
	if cmd.Prompt == "" {
		return Receipt{}, errors.New("请说一下具体要做什么")
	}
	if utf8.RuneCountInString(cmd.Prompt) > 2000 {
		return Receipt{}, errors.New("需求太长，请控制在两千字以内")
	}
	c.submitMu.Lock()
	defer c.submitMu.Unlock()
	if c.ctx.Err() != nil {
		return Receipt{}, errors.New("服务正在关闭，请稍后再试")
	}
	s := c.State.Get()
	message := "好的，已转达"
	switch {
	case cmd.Action == "start":
		message = "好的，开个新项目。云端准备环境约需半分钟"
	case c.pending > 0 || s.Status == "creating" || s.Status == "sending" || s.Status == "running":
		message = "好的，先停下当前的再按你说的做"
	case s.SessionID == "" || s.Status == "terminated":
		message = "好的，开始做。云端准备环境约需半分钟"
	}
	cmd.ID = "cmd_" + agentapi.ID()
	select {
	case c.queue <- cmd:
		c.pending++
	default:
		return Receipt{}, ErrQueueFull
	}
	slog.Info("command accepted", "command_id", cmd.ID, "source", cmd.Source, "linx_session_id", cmd.ConversationID)
	return Receipt{true, message}, nil
}
func (c *Controller) StatusText() string { return progress.SpokenStatus(c.State.Get()) }
func (c *Controller) work() {
	defer c.wg.Done()
	defer func() {
		if c.streamCancel != nil {
			c.streamCancel()
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.publishCancel != nil {
			c.publishCancel()
		}
	}()
	for {
		select {
		case <-c.ctx.Done():
			return
		case cmd := <-c.queue:
			err := c.execute(c.ctx, cmd)
			if err != nil && c.ctx.Err() == nil {
				slog.Error("command failed", "command_id", cmd.ID, "action", cmd.Action, "error", err)
				c.mu.Lock()
				c.State.Update(func(s *device.Snapshot) {
					s.Error = err.Error()
					s.Summary = "操作未完成，请查看错误信息"
					s.Status = "error"
				})
				c.mu.Unlock()
			}
			c.submitMu.Lock()
			c.pending--
			c.submitMu.Unlock()
		}
	}
}
func (c *Controller) status(ctx context.Context, id string) (agentapi.Session, error) {
	ctx, cancel := context.WithTimeout(ctx, c.options.RequestTimeout)
	defer cancel()
	s, err := c.Provider.Status(ctx, id)
	if err == nil && !agentapi.KnownStatus(s.Status) {
		err = fmt.Errorf("AgentAPI 返回未知会话状态")
	}
	return s, err
}
func (c *Controller) waitIdle(ctx context.Context, id string) (agentapi.Session, error) {
	ticker := time.NewTicker(c.options.PollInterval)
	defer ticker.Stop()
	for {
		s, err := c.status(ctx, id)
		if agentapi.IsStatus(err, 404) {
			return agentapi.Session{ID: id, Status: "terminated"}, nil
		}
		if err == nil && (s.Status == "idle" || s.Status == "terminated") {
			return s, nil
		}
		if err != nil && !agentapi.Retryable(err) {
			return agentapi.Session{}, err
		}
		select {
		case <-ctx.Done():
			return agentapi.Session{}, fmt.Errorf("等待 agent 停止: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}
func (c *Controller) stop(ctx context.Context, id string) (agentapi.Session, error) {
	ctx, cancel := context.WithTimeout(ctx, c.options.StopTimeout)
	defer cancel()
	s, err := c.status(ctx, id)
	if agentapi.IsStatus(err, 404) {
		return agentapi.Session{ID: id, Status: "terminated"}, nil
	}
	if err != nil {
		return agentapi.Session{}, err
	}
	if s.Status == "idle" || s.Status == "terminated" {
		return s, nil
	}
	// The stop budget owns the interrupt request. Do not cancel a returned
	// acknowledgement context before the runtime has actually transitioned.
	err = c.Provider.Interrupt(ctx, id)
	if err != nil && !agentapi.Uncertain(err) {
		return agentapi.Session{}, err
	}
	return c.waitIdle(ctx, id)
}
func (c *Controller) reconcile(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, c.options.ReconcileTimeout)
	defer cancel()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		c.mu.Lock()
		uncertain := c.current.uncertain
		creation := c.createUncertain
		c.mu.Unlock()
		if creation {
			return errors.New("创建结果未知，请核对云端会话后重启控制服务")
		}
		if !uncertain {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("上次需求的提交结果尚未确认，请等待事件恢复后重试")
		case <-tick.C:
		}
	}
}
func (c *Controller) execute(ctx context.Context, cmd Command) error {
	if err := c.reconcile(ctx); err != nil {
		return err
	}
	c.mu.Lock()
	c.turnGeneration++
	c.current = turn{}
	if c.publishCancel != nil {
		c.publishCancel()
		c.publishCancel = nil
	}
	s := c.State.Get()
	c.mu.Unlock()
	if s.SessionID != "" {
		current, err := c.stop(ctx, s.SessionID)
		if err != nil {
			return err
		}
		if cmd.Action == "message" && current.Status == "idle" {
			return c.send(ctx, s.SessionID, cmd)
		}
	}
	if c.streamCancel != nil {
		c.streamCancel()
	}
	c.mu.Lock()
	c.sessionGeneration++
	generation := c.sessionGeneration
	c.State.Update(func(s *device.Snapshot) {
		s.SessionID = ""
		s.Status = "creating"
		s.Summary = "正在准备云端环境"
		s.Prompt = cmd.Prompt
		s.ResultURL = ""
		s.Error = ""
		s.PreviewError = ""
		s.StreamError = ""
	})
	c.mu.Unlock()
	createCtx, cancel := context.WithTimeout(ctx, c.options.CreateTimeout)
	created, err := c.Provider.Create(createCtx)
	cancel()
	if err != nil {
		c.mu.Lock()
		c.createUncertain = agentapi.Uncertain(err)
		c.mu.Unlock()
		return err
	}
	if created.ID == "" {
		c.mu.Lock()
		c.createUncertain = true
		c.mu.Unlock()
		return errors.New("创建结果未知：云端未返回会话编号")
	}
	c.mu.Lock()
	c.State.Update(func(s *device.Snapshot) { s.SessionID = created.ID; s.Status = "idle" })
	c.mu.Unlock()
	streamCtx, streamCancel := context.WithCancel(c.ctx)
	c.streamCancel = streamCancel
	c.wg.Add(1)
	go c.observe(streamCtx, created.ID, generation)
	return c.send(ctx, created.ID, cmd)
}
func (c *Controller) send(ctx context.Context, id string, cmd Command) error {
	c.mu.Lock()
	c.current = turn{command: cmd.ID}
	c.State.Update(func(s *device.Snapshot) {
		s.Prompt = cmd.Prompt
		s.Status = "sending"
		s.Summary = "正在提交需求"
		s.Error = ""
		s.PreviewError = ""
		s.ResultURL = ""
	})
	c.mu.Unlock()
	requestCtx, cancel := context.WithTimeout(ctx, c.options.RequestTimeout)
	var accepted agentapi.AcceptedInput
	var err error
	correlated := false
	if provider, ok := c.Provider.(agentapi.AcceptedProvider); ok {
		correlated = true
		accepted, err = provider.SendMessage(requestCtx, id, agentapi.Message{Text: cmd.Prompt, CommandID: cmd.ID})
	} else {
		err = c.Provider.Send(requestCtx, id, cmd.Prompt)
		accepted.EventID = ""
	}
	cancel()
	c.mu.Lock()
	c.current.event = accepted.EventID
	// The entire turn may already be in the log when the acceptance response arrives.
	if !c.current.seen && accepted.EventID != "" {
		for _, f := range c.Events.Frames(id) {
			c.reduceLocked(id, c.sessionGeneration, f)
		}
	}
	observed := c.current.seen
	if correlated && err != nil && !observed && agentapi.Uncertain(err) {
		c.current.uncertain = true
	}
	if correlated && err == nil && accepted.EventID == "" && !observed {
		c.current.uncertain = true
	}
	uncertain := c.current.uncertain
	if (err == nil || observed) && !uncertain {
		c.State.Update(func(s *device.Snapshot) {
			if s.Status == "sending" {
				s.Status = "running"
				s.Summary = "正在处理你的需求"
			}
			s.Error = ""
		})
	}
	c.mu.Unlock()
	slog.Info("message submitted", "command_id", cmd.ID, "session_id", id, "event_id", accepted.EventID, "observed", observed, "uncertain", uncertain)
	if observed {
		return nil
	}
	if uncertain {
		return c.reconcile(ctx)
	}
	return err
}
