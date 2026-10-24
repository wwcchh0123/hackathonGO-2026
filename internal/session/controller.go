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

	"github.com/wwcchh0123/hackathonGO-2026/internal/agentapi"
	"github.com/wwcchh0123/hackathonGO-2026/internal/artifacts"
	"github.com/wwcchh0123/hackathonGO-2026/internal/device"
	"github.com/wwcchh0123/hackathonGO-2026/internal/events"
	"github.com/wwcchh0123/hackathonGO-2026/internal/progress"
)

var ErrQueueFull = errors.New("指令队列已满，请稍后重试")

type Command struct {
	Action string      `json:"action"`
	Prompt string      `json:"prompt,omitempty"`
	View   device.View `json:"view,omitempty"`
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
	starting     bool
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
	switch cmd.Action {
	case "start":
		if cmd.Prompt == "" {
			return Receipt{}, errors.New("请描述要做的任务")
		}
	case "interrupt", "continue":
	case "show":
		if !cmd.View.Valid() {
			return Receipt{}, errors.New("未知视图")
		}
	default:
		return Receipt{}, errors.New("未知指令")
	}
	if len(cmd.Prompt) > 16000 {
		return Receipt{}, errors.New("需求过长")
	}
	c.submitMu.Lock()
	defer c.submitMu.Unlock()
	if cmd.Action == "start" && c.starting {
		return Receipt{}, errors.New("正在创建会话，请稍候")
	}
	if cmd.Action != "start" && cmd.Action != "show" && c.State.Get().SessionID == "" && !c.starting {
		return Receipt{}, errors.New("请先开始一个任务")
	}
	if c.ctx.Err() != nil {
		return Receipt{}, errors.New("服务正在关闭")
	}
	select {
	case c.queue <- cmd:
		if cmd.Action == "start" {
			c.starting = true
		}
	default:
		return Receipt{}, ErrQueueFull
	}
	message := map[string]string{"start": "好的，开始做", "interrupt": "好的，正在调整", "continue": "好的，继续", "show": "好的，切换画面"}[cmd.Action]
	return Receipt{Accepted: true, Message: message}, nil
}
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
			if cmd.Action == "start" {
				c.submitMu.Lock()
				c.starting = false
				c.submitMu.Unlock()
			}
			if err != nil && c.ctx.Err() == nil {
				slog.Error("command failed", "action", cmd.Action, "error", err)
				c.State.Update(func(s *device.Snapshot) {
					s.Error = err.Error()
					s.Summary = "操作未完成：" + err.Error()
					if s.Status == "creating" {
						s.Status = "error"
					}
				})
			}
		}
	}
}

// waitIdle reads authoritative provider state. An HTTP interrupt acknowledgement
// or an old idle event replay is never sufficient to send the next message.
func (c *Controller) waitIdle(ctx context.Context, id string) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		s, err := c.Provider.Status(ctx, id)
		if err != nil {
			return err
		}
		if s.Status == "idle" {
			return nil
		}
		if s.Status == "terminated" {
			return errors.New("会话已终止，请开始新任务")
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("等待 agent 停止: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}
func (c *Controller) stop(ctx context.Context, id string) error {
	s, err := c.Provider.Status(ctx, id)
	if err != nil {
		return err
	}
	if s.Status == "idle" || s.Status == "terminated" {
		return nil
	}
	if err := c.Provider.Interrupt(ctx, id); err != nil {
		return err
	}
	return c.waitIdle(ctx, id)
}
func (c *Controller) execute(ctx context.Context, cmd Command) error {
	s := c.State.Get()
	if cmd.Action == "show" {
		if cmd.View == device.Preview && s.ResultURL == "" {
			return errors.New("结果尚未生成")
		}
		c.State.Update(func(s *device.Snapshot) { s.View = cmd.View; s.Error = "" })
		return nil
	}
	c.epoch.Add(1)
	if cmd.Action == "start" {
		if s.SessionID != "" {
			if err := c.stop(ctx, s.SessionID); err != nil {
				return err
			}
		}
		if c.streamCancel != nil {
			c.streamCancel()
		}
		c.State.Update(func(s *device.Snapshot) {
			s.SessionID = ""
			s.Status = "creating"
			s.Summary = "正在准备云端工作空间"
			s.Prompt = cmd.Prompt
			s.ResultURL = ""
			s.Error = ""
			s.View = device.Timeline
		})
		created, err := c.Provider.Create(ctx)
		if err != nil {
			return err
		}
		s.SessionID = created.ID
		c.State.Update(func(s *device.Snapshot) { s.SessionID = created.ID; s.Status = "idle" })
		streamCtx, cancel := context.WithCancel(c.ctx)
		c.streamCancel = cancel
		c.wg.Add(1)
		go c.observe(streamCtx, created.ID)
		return c.send(ctx, created.ID, cmd.Prompt)
	}
	if s.SessionID == "" {
		return errors.New("当前没有会话，请重新开始任务")
	}
	if cmd.Action == "interrupt" {
		if err := c.stop(ctx, s.SessionID); err != nil {
			return err
		}
		if cmd.Prompt == "" {
			c.State.Update(func(s *device.Snapshot) {
				s.Status = "idle"
				s.Summary = "已暂停，等待你的下一步指令"
				s.Error = ""
			})
			return nil
		}
		return c.send(ctx, s.SessionID, cmd.Prompt)
	}
	current, err := c.Provider.Status(ctx, s.SessionID)
	if err != nil {
		return err
	}
	if current.Status != "idle" {
		return errors.New("会话正在执行或已终止，请暂停后继续，或开始新任务")
	}
	return c.send(ctx, s.SessionID, "继续")
}
func (c *Controller) send(ctx context.Context, id, prompt string) error {
	c.epoch.Add(1)
	c.State.Update(func(s *device.Snapshot) {
		s.Prompt = prompt
		s.Status = "sending"
		s.Summary = "正在提交需求"
		s.Error = ""
		s.ResultURL = ""
		if s.View == device.Preview {
			s.View = device.Timeline
		}
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
	if c.epoch.Load() != epoch {
		return
	}
	c.State.Update(func(s *device.Snapshot) {
		if s.SessionID != id {
			return
		}
		if err != nil {
			s.Error = "发布预览失败：" + err.Error()
			return
		}
		s.ResultURL = result
	})
}
