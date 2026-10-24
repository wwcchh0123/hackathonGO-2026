package progress

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/wwcchh0123/hackathonGO-2026/internal/device"
	"github.com/wwcchh0123/hackathonGO-2026/internal/events"
)

func TestStatusReportsStopReasonWithoutClaimingSuccess(t *testing.T) {
	for _, tt := range []struct{ reason, want string }{
		{"end_turn", "本轮已结束"}, {"interrupted", "已暂停"}, {"requires_action", "正在等待进一步操作"}, {"retries_exhausted", "重试已耗尽"}, {"budget_reached", "已达到执行预算"}, {"unknown", "正在等待下一步指令"},
	} {
		body, _ := json.Marshal(map[string]any{"type": "session.status_idle", "stop_reason": map[string]string{"type": tt.reason}})
		summary := Summary(events.Frame{Data: body})
		spoken := SpokenStatus(device.Snapshot{Status: "idle", Summary: summary})
		if !strings.Contains(spoken, tt.want) || strings.Contains(spoken, "屏幕") {
			t.Errorf("%s: %s", tt.reason, spoken)
		}
	}
}
func TestSpokenStatusDoesNotLeakProviderPayload(t *testing.T) {
	for _, summary := range []string{"打开 https://example.test/result", "session_id: ses_private", "{\"id\":\"private\"}", "任务 ABC123 已结束", strings.Repeat("准备页面", 30)} {
		got := SpokenStatus(device.Snapshot{Status: "idle", Summary: summary, ResultURL: "https://example.test/secret"})
		if utf8.RuneCountInString(got) > 60 || strings.ContainsAny(got, "{}:/_") || strings.Contains(got, "ABC123") {
			t.Fatal(got)
		}
		if !strings.Contains(got, "结果已经在屏幕上了") {
			t.Fatal(got)
		}
	}
	if got := SpokenStatus(device.Snapshot{Status: "running", Summary: "正在写代码", Error: "api-key=secret"}); !strings.Contains(got, "操作未完成") || strings.Contains(got, "secret") {
		t.Fatal(got)
	}
}
