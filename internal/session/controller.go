// Package session owns all programming decisions and serializes control commands.
package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
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
	Action string `json:"action"`
	Prompt string `json:"prompt,omitempty"`
}
type Receipt struct {
	Accepted bool   `json:"accepted"`
	Message  string `json:"message"`
}
type Controller struct {
	Provider     agentapi.Provider
	State        *device.State
	Events       *events.Hub
	store        *artifacts.Store
	ctx          context.Context
	queue        chan Command
	wg           sync.WaitGroup
	streamCancel context.CancelFunc // only the command worker writes this
	epoch        atomic.Uint64
	submitMu     sync.Mutex
	pending      int // queued and executing commands; guarded by submitMu
}

func New(ctx context.Context, p agentapi.Provider, state *device.State, store *artifacts.Store) *Controller {
	c := &Controller{Provider: p, State: state, Events: events.NewHub(), store: store, ctx: ctx, queue: make(chan Command, 32)}
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
	snapshot := c.State.Get()
	message := "好的，已转达"
	switch {
	case cmd.Action == "start":
		message = "好的，开个新项目。云端准备环境约需半分钟"
	case c.pending > 0 || snapshot.Status == "creating" || snapshot.Status == "sending" || snapshot.Status == "running":
		message = "好的，先停下当前的再按你说的做"
	case snapshot.SessionID == "" || snapshot.Status == "terminated":
		message = "好的，开始做。云端准备环境约需半分钟"
	}
	select {
	case c.queue <- cmd:
		c.pending++
	default:
		return Receipt{}, ErrQueueFull
	}
	return Receipt{Accepted: true, Message: message}, nil
}

// StatusText reads only the device snapshot; it never waits on the provider.
func (c *Controller) StatusText() string { return progress.SpokenStatus(c.State.Get()) }

func (c *Controller) work() {
	defer c.wg.Done()
	defer func() {
		if c.streamCancel != nil {
			c.streamCancel()
		}
	}()
	for {
		select {
		case <-c.ctx.Done():
			return
		case cmd := <-c.queue:
			ctx, cancel := context.WithTimeout(c.ctx, 60*time.Second)
			err := c.execute(ctx, cmd)
			cancel()
			if err != nil && c.ctx.Err() == nil {
				slog.Error("command failed", "action", cmd.Action, "error", err)
				c.State.Update(func(s *device.Snapshot) {
					s.Error = err.Error()
					s.Summary = "操作未完成：" + err.Error()
					s.Status = "error"
				})
			}
			c.submitMu.Lock()
			c.pending--
			c.submitMu.Unlock()
		}
	}
}

// waitIdle reads authoritative provider state. An HTTP interrupt acknowledgement
// or an old idle event replay is never sufficient to send the next message.
func (c *Controller) waitIdle(ctx context.Context, id string) (agentapi.Session, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		s, err := c.Provider.Status(ctx, id)
		if err != nil {
			return agentapi.Session{}, err
		}
		if s.Status == "idle" || s.Status == "terminated" {
			return s, nil
		}
		select {
		case <-ctx.Done():
			return agentapi.Session{}, fmt.Errorf("等待 agent 停止: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}
func (c *Controller) stop(ctx context.Context, id string) (agentapi.Session, error) {
	s, err := c.Provider.Status(ctx, id)
	if err != nil {
		return agentapi.Session{}, err
	}
	if s.Status == "idle" || s.Status == "terminated" {
		return s, nil
	}
	if err := c.Provider.Interrupt(ctx, id); err != nil {
		return agentapi.Session{}, err
	}
	return c.waitIdle(ctx, id)
}
func (c *Controller) execute(ctx context.Context, cmd Command) error {
	// Resolve the binding and authoritative runtime state at execution time,
	// including commands accepted while a preceding Create was still pending.
	s := c.State.Get()
	c.epoch.Add(1)
	if s.SessionID != "" {
		current, err := c.stop(ctx, s.SessionID)
		if err != nil {
			return err
		}
		if cmd.Action == "message" && current.Status == "idle" {
			return c.send(ctx, s.SessionID, cmd.Prompt)
		}
	}
	if c.streamCancel != nil {
		c.streamCancel()
	}
	c.State.Update(func(s *device.Snapshot) {
		s.SessionID = ""
		s.Status = "creating"
		s.Summary = "正在准备云端环境"
		s.Prompt = cmd.Prompt
		s.ResultURL = ""
		s.Error = ""
	})
	created, err := c.Provider.Create(ctx)
	if err != nil {
		return err
	}
	c.State.Update(func(s *device.Snapshot) { s.SessionID = created.ID; s.Status = "idle" })
	streamCtx, cancel := context.WithCancel(c.ctx)
	c.streamCancel = cancel
	c.wg.Add(1)
	go c.observe(streamCtx, created.ID)
	return c.send(ctx, created.ID, cmd.Prompt)
}
func (c *Controller) send(ctx context.Context, id, prompt string) error {
	c.epoch.Add(1)
	c.State.Update(func(s *device.Snapshot) {
		s.Prompt = prompt
		s.Status = "sending"
		s.Summary = "正在提交需求"
		s.Error = ""
		s.ResultURL = ""
	})
	if err := c.Provider.Send(ctx, id, prompt); err != nil {
		c.State.Update(func(s *device.Snapshot) {
			if s.Status == "sending" {
				s.Status = "error"
			}
		})
		return err
	}
	c.State.Update(func(s *device.Snapshot) {
		// Do not overwrite a terminal event that arrived before Send returned.
		if s.Status == "sending" {
			s.Status = "running"
			s.Summary = "正在处理你的需求"
		}
	})
	return nil
}
func (c *Controller) observe(ctx context.Context, id string) {
	defer c.wg.Done()
	for ctx.Err() == nil {
		err := c.Provider.Stream(ctx, id, func(f events.Frame) error {
			if !c.Events.Append(id, f) {
				return nil
			}
			if c.State.Get().SessionID != id {
				return nil
			}
			summary := progress.Summary(f)
			c.State.Update(func(s *device.Snapshot) {
				if s.SessionID != id {
					return
				}
				if summary != "" {
					s.Summary = summary
				}
				switch f.Type {
				case "session.status_running":
					s.Status = "running"
				case "session.status_idle":
					s.Status = "idle"
				case "session.status_terminated":
					s.Status = "terminated"
				}
			})
			if progress.EndTurn(f) {
				c.publish(ctx, id)
			}
			return nil
		})
		if ctx.Err() != nil {
			return
		}
		slog.Warn("provider event stream disconnected; replaying on reconnect", "session", id, "error", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}
func (c *Controller) publish(ctx context.Context, id string) {
	epoch := c.epoch.Load()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	status, err := c.Provider.Status(ctx, id)
	if err != nil || status.Status != "idle" {
		return
	}
	files, err := c.Provider.Artifacts(ctx, id)
	var result string
	if err == nil {
		result, err = c.store.Publish(ctx, files)
	}
	c.State.Update(func(s *device.Snapshot) {
		if c.epoch.Load() != epoch || s.SessionID != id {
			return
		}
		if err != nil {
			s.Error = "发布预览失败：" + err.Error()
			return
		}
		s.ResultURL = result
	})
}
