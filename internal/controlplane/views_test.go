package controlplane

import (
	"testing"
	"time"

	"github.com/wwcchh0123/hackathonGO-2026/internal/progress"
	"github.com/wwcchh0123/hackathonGO-2026/internal/session"
)

func TestViewValidationAndMatchingHelpers(t *testing.T) {
	deviceOptions := DeviceListOptions{}
	if err := validateDeviceList(&deviceOptions); err != nil || deviceOptions.Limit != 20 || deviceOptions.Sort != "attention" {
		t.Fatalf("device defaults: %+v %v", deviceOptions, err)
	}
	for _, health := range []string{"online", "offline", "unseen", "attention"} {
		o := DeviceListOptions{Health: health}
		if err := validateDeviceList(&o); err != nil {
			t.Fatalf("health %s: %v", health, err)
		}
	}
	for _, invalid := range []DeviceListOptions{{Limit: 0, Sort: "bad"}, {Limit: 101}, {Health: "bad"}, {Query: string(make([]byte, 201))}} {
		if err := validateDeviceList(&invalid); err == nil {
			t.Fatalf("invalid device options accepted: %+v", invalid)
		}
	}
	taskOptions := TaskListOptions{}
	if err := validateTaskList(&taskOptions); err != nil || taskOptions.Limit != 20 {
		t.Fatalf("task defaults: %+v %v", taskOptions, err)
	}
	for _, status := range []string{"idle", "running", "rescheduling", "terminated"} {
		o := TaskListOptions{Status: status}
		if err := validateTaskList(&o); err != nil {
			t.Fatalf("status %s: %v", status, err)
		}
	}
	for _, invalid := range []TaskListOptions{{Limit: 101}, {Status: "bad"}, {DeviceID: string(make([]byte, 201))}} {
		if err := validateTaskList(&invalid); err == nil {
			t.Fatalf("invalid task options accepted: %+v", invalid)
		}
	}
	for _, test := range []struct {
		raw  string
		want int
	}{{"", 0}, {"3", 3}} {
		got, err := pageOffset(test.raw)
		if err != nil || got != test.want {
			t.Fatalf("page %q: %d %v", test.raw, got, err)
		}
	}
	for _, raw := range []string{"x", "-1", "1000001"} {
		if _, err := pageOffset(raw); err == nil {
			t.Fatalf("invalid page accepted: %q", raw)
		}
	}

	now := time.Now().UTC()
	base := DeviceOverview{DeviceID: "device-a", LastSeen: &now, Online: true}
	if deviceHealth(base) != "online" {
		t.Fatal("online health mismatch")
	}
	base.Online = false
	if deviceHealth(base) != "offline" {
		t.Fatal("offline health mismatch")
	}
	base.LastSeen = nil
	if deviceHealth(base) != "unseen" {
		t.Fatal("unseen health mismatch")
	}
	base.Attention = []string{"error"}
	if deviceHealth(base) != "attention" {
		t.Fatal("attention health mismatch")
	}
	item := AdminTask{Task: session.Task{Ref: "task-a", SessionID: "ses-a", Title: "Build dashboard", Summary: &progress.Message{Text: "running dashboard"}}, DeviceID: "device-a"}
	for _, query := range []string{"task-a", "ses-a", "dashboard", "running", "device-a"} {
		if !taskMatch(item, query) {
			t.Fatalf("query %q did not match task", query)
		}
	}
	if taskMatch(item, "other") || !taskMatch(item, "") {
		t.Fatal("task matching boundaries are wrong")
	}
}
