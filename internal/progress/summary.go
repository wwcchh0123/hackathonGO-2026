// Package progress derives short spoken summaries without an extra model call.
package progress

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/wwcchh0123/hackathonGO-2026/internal/events"
)

type Event struct {
	Type    string `json:"type"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
		Name string `json:"name"`
	} `json:"content"`
	StopReason struct {
		Type string `json:"type"`
	} `json:"stop_reason"`
}

func Summary(f events.Frame) string {
	var e Event
	if json.Unmarshal(f.Data, &e) != nil {
		return ""
	}
	switch e.Type {
	case "session.status_running":
		return "正在处理你的需求"
	case "session.status_rescheduled":
		return "正在恢复执行，请稍候"
	case "session.status_terminated":
		return "会话已终止，请开始新任务"
	case "session.status_idle":
		switch e.StopReason.Type {
		case "end_turn":
			return "本轮已结束，可以查看结果或继续修改"
		case "interrupted":
			return "已暂停，等待你的下一步指令"
		case "requires_action":
			return "正在等待进一步操作"
		case "retries_exhausted":
			return "重试已耗尽，请检查错误后继续"
		case "budget_reached":
			return "已达到执行预算，可以检查已有结果"
		default:
			return "正在等待下一步指令"
		}
	case "agent.message":
		for _, c := range e.Content {
			if c.Type == "text" && strings.TrimSpace(c.Text) != "" {
				return truncate(c.Text, 48)
			}
		}
	case "agent.tool_use":
		return "正在执行工具操作"
	}
	return ""
}
func EndTurn(f events.Frame) bool {
	var e Event
	return json.Unmarshal(f.Data, &e) == nil && e.Type == "session.status_idle" && e.StopReason.Type == "end_turn"
}
func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > n {
		return string([]rune(s)[:n]) + "…"
	}
	return s
}
