package session

import (
	"context"
	"encoding/json"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/wwcchh0123/hackathonGO-2026/internal/agentapi"
	"github.com/wwcchh0123/hackathonGO-2026/internal/device"
	"github.com/wwcchh0123/hackathonGO-2026/internal/events"
	"github.com/wwcchh0123/hackathonGO-2026/internal/progress"
)

func (c *Controller) observe(ctx context.Context, id string, generation uint64) {
	defer c.wg.Done()
	delay := time.Second
	for ctx.Err() == nil {
		err := c.Provider.Stream(ctx, id, func(f events.Frame) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			c.mu.Lock()
			defer c.mu.Unlock()
			if generation != c.sessionGeneration || c.State.Get().SessionID != id {
				return nil
			}
			c.State.Update(func(s *device.Snapshot) { s.StreamError = "" })
			delay = time.Second
			if c.Events.Append(id, f) {
				c.reduceLocked(id, generation, f)
			}
			return nil
		})
		if ctx.Err() != nil {
			return
		}
		permanent := agentapi.IsStatus(err, 401) || agentapi.IsStatus(err, 403) || agentapi.IsStatus(err, 404)
		c.mu.Lock()
		if generation == c.sessionGeneration && c.State.Get().SessionID == id {
			c.State.Update(func(s *device.Snapshot) {
				s.StreamError = "进度连接中断，正在重连"
				if permanent {
					s.StreamError = "进度连接不可用，请检查云端配置"
				}
			})
		}
		c.mu.Unlock()
		slog.Warn("provider stream disconnected", "session_id", id, "error", err, "retry", !permanent)
		if permanent {
			return
		}
		timer := time.NewTimer(delay + time.Duration(rand.Int64N(int64(delay/4)+1)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		delay = min(10*time.Second, delay*2)
	}
}

// reduceLocked only changes the current turn after its accepted input boundary.
// All checks and snapshot writes share mu; the network is never called here.
func (c *Controller) reduceLocked(id string, generation uint64, f events.Frame) {
	if generation != c.sessionGeneration || c.State.Get().SessionID != id || c.current.command == "" {
		return
	}
	var meta struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Request string `json:"client_request_id"`
	}
	if json.Unmarshal(f.Data, &meta) != nil {
		return
	}
	if meta.Type == "user.message" && ((meta.Request != "" && meta.Request == c.current.command) || (c.current.event != "" && meta.ID == c.current.event)) {
		c.current.seen = true
		c.current.uncertain = false
		c.State.Update(func(s *device.Snapshot) {
			s.Error = ""
			if s.Status == "error" || s.Status == "sending" {
				s.Status = "running"
				s.Summary = "正在处理你的需求"
			}
		})
		return
	}
	if !c.current.seen || c.current.stopped {
		return
	}
	summary := progress.Summary(f)
	c.State.Update(func(s *device.Snapshot) {
		if summary != "" {
			s.Summary = summary
		}
		switch meta.Type {
		case "session.status_running", "session.status_rescheduled":
			s.Status = "running"
		case "session.status_idle":
			s.Status = "idle"
		case "session.status_terminated":
			s.Status = "terminated"
		}
	})
	if meta.Type == "session.status_idle" || meta.Type == "session.status_terminated" {
		c.current.stopped = true
		c.current.terminal = meta.ID
		slog.Info("turn stopped", "command_id", c.current.command, "session_id", id, "event_id", meta.ID, "end_turn", progress.EndTurn(f))
		if progress.EndTurn(f) {
			publishCtx, cancel := context.WithTimeout(c.ctx, c.options.PublishTimeout)
			c.publishCancel = cancel
			c.wg.Add(1)
			go c.publish(publishCtx, cancel, id, generation, c.turnGeneration, meta.ID)
		}
	}
}
