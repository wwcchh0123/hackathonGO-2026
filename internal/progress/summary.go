// Package progress derives short spoken summaries without an extra model call.
package progress

import (
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/wwcchh0123/hackathonGO-2026/internal/device"
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

// SpokenStatus is deliberately conservative about free-form provider text:
// voice tools never read technical identifiers, URLs, JSON, or raw errors aloud.
func SpokenStatus(s device.Snapshot) string {
	if s.Error != "" || s.Status == "error" {
		return "操作未完成，请检查设备上的错误后重试"
	}
	var text string
	switch s.Status {
	case "creating":
		text = "正在准备云端环境"
	case "sending", "running":
		text = "正在做：" + spokenSummary(s.Summary, "正在处理你的需求")
	case "idle":
		text = spokenSummary(s.Summary, "正在等待下一步指令")
		if s.ResultURL != "" {
			text += "。结果已经在屏幕上了"
		}
	case "terminated":
		text = "任务已结束，再说个需求就会开始新任务"
	default:
		text = "还没有任务，可以告诉我想做什么"
	}
	return truncate(text, 59)
}
func spokenSummary(s, fallback string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return fallback
	}
	for _, r := range s {
		if !unicode.Is(unicode.Han, r) && !unicode.IsSpace(r) && !strings.ContainsRune("，。！？、；：…（）", r) {
			return fallback
		}
	}
	return strings.TrimRight(truncate(s, 42), "。")
}
