package controlplane

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/wwcchh0123/hackathonGO-2026/internal/agentapi"
	"github.com/wwcchh0123/hackathonGO-2026/internal/session"
)

type failedSendProvider struct{ agentapi.Provider }

func (p *failedSendProvider) SendMessage(context.Context, string, agentapi.Message) (agentapi.AcceptedInput, error) {
	return agentapi.AcceptedInput{}, errors.New("simulated send failure")
}

func waitCommand(t *testing.T, c *session.Controller, receipt session.Receipt) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		zero := 0
		detail, err := c.GetTask(context.Background(), receipt.TaskRef, session.DetailOptions{CommandID: receipt.CommandID, EventsLimit: &zero})
		if err != nil {
			t.Fatal(err)
		}
		if detail.Command != nil && detail.Command.State == "confirmed" {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("command did not confirm")
}

func TestManagerRoutesDevicesAndAggregatesOwnedTasks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	mock := agentapi.NewMock(ctx)
	mock.Step = time.Hour
	m := New(ctx, mock, "a", t.TempDir(), "http://example.test", Options{OnlineWindow: time.Second})
	defer func() { cancel(); m.Wait() }()
	if err := m.Touch("a", "test"); err != nil {
		t.Fatal(err)
	}
	if err := m.Touch("b", "test"); err != nil {
		t.Fatal(err)
	}
	ca := m.DefaultController()
	cb, ok := m.Controller("b")
	if !ok || cb == ca {
		t.Fatal("devices did not get independent controllers")
	}
	ra, err := ca.Submit(session.Command{Action: "start", Prompt: "alpha-task"})
	if err != nil {
		t.Fatal(err)
	}
	rb, err := cb.Submit(session.Command{Action: "start", Prompt: "beta-task"})
	if err != nil {
		t.Fatal(err)
	}
	waitCommand(t, ca, ra)
	waitCommand(t, cb, rb)
	devices, queried := m.ListDevices(ctx)
	if queried.IsZero() || len(devices) != 2 {
		t.Fatal(devices, queried)
	}
	for _, d := range devices {
		if d.TaskCounts.Total != 1 || d.TasksError != "" || d.LastSeen == nil || !d.Online {
			t.Fatalf("unexpected overview: %+v", d)
		}
	}
	if _, err := m.Snapshot("b"); err != nil {
		t.Fatal(err)
	}
	dashboard, err := m.Dashboard(ctx)
	if err != nil || dashboard.DeviceCount != 2 || dashboard.RunningTasks != 2 {
		t.Fatal("dashboard did not aggregate devices/tasks", dashboard, err)
	}
	searched, err := m.DashboardQuery(ctx, "alpha-task")
	if err != nil || len(searched.Tasks) != 1 || searched.Tasks[0].DeviceID != "a" {
		t.Fatal("dashboard query did not locate task", searched, err)
	}
	if _, err := m.ListTasks(ctx, "unknown", session.ListOptions{}); !errors.Is(err, ErrUnknownDevice) {
		t.Fatal(err)
	}
	if _, err := m.GetTask(ctx, "b", ra.TaskRef, session.DetailOptions{EventsLimit: ptr(0)}); !errors.Is(err, session.ErrReference) {
		t.Fatal("cross-device task reference was accepted", err)
	}
	if err := m.Touch("bad/id", "test"); !errors.Is(err, ErrUnknownDevice) {
		t.Fatal("invalid device id was accepted", err)
	}
}

func TestDefaultDeviceIncludesLegacySessionsOnlyForDefault(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	mock := agentapi.NewMock(ctx)
	m := New(ctx, mock, "default", t.TempDir(), "http://example.test")
	defer func() { cancel(); m.Wait() }()
	if _, err := mock.Create(ctx, agentapi.NewSession{Title: "legacy"}); err != nil {
		t.Fatal(err)
	}
	if err := m.Touch("other", "test"); err != nil {
		t.Fatal(err)
	}
	defaultTasks, err := m.ListTasks(ctx, "default", session.ListOptions{})
	if err != nil || len(defaultTasks.Data) != 1 {
		t.Fatal(defaultTasks, err)
	}
	otherTasks, err := m.ListTasks(ctx, "other", session.ListOptions{})
	if err != nil || len(otherTasks.Data) != 0 {
		t.Fatal(otherTasks, err)
	}
}

func TestAdminViewsSearchAndAttentionReasons(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	mock := agentapi.NewMock(ctx)
	mock.Step = time.Hour
	m := New(ctx, mock, "default", t.TempDir(), "http://example.test", Options{OnlineWindow: time.Second})
	defer func() { cancel(); m.Wait() }()
	if err := m.Touch("other", "mcp"); err != nil {
		t.Fatal(err)
	}
	c := m.DefaultController()
	receipt, err := c.Submit(session.Command{Action: "start", Prompt: "searchable task"})
	if err != nil {
		t.Fatal(err)
	}
	// The mock is intentionally held running, so the default device has an
	// active task that becomes actionable once the control-plane touch expires.
	waitCommand(t, c, receipt)
	time.Sleep(1100 * time.Millisecond)
	page, err := m.ListAdminTasks(ctx, TaskListOptions{Query: "searchable", Limit: 10})
	if err != nil || len(page.Data) != 1 || page.Data[0].DeviceID != "default" {
		t.Fatal(page, err)
	}
	if len(page.Data[0].Attention) == 0 || page.Data[0].Attention[0] != "设备离线但任务仍运行" {
		t.Fatalf("running task attention: %+v", page.Data[0])
	}
	devices, err := m.ListDeviceViews(ctx, DeviceListOptions{Health: "attention", Limit: 10})
	if err != nil || len(devices.Data) != 1 || devices.Data[0].DeviceID != "default" {
		t.Fatal(devices, err)
	}
	if _, err := m.ListAdminTasks(ctx, TaskListOptions{Status: "bad"}); err == nil {
		t.Fatal("unknown task status accepted")
	}
}

func TestAdminViewsExposeFailedCommandAttention(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	mock := agentapi.NewMock(ctx)
	p := &failedSendProvider{Provider: mock}
	m := New(ctx, p, "default", t.TempDir(), "http://example.test", Options{OnlineWindow: time.Second})
	defer func() { cancel(); m.Wait() }()
	receipt, err := m.DefaultController().Submit(session.Command{Action: "start", Prompt: "will fail"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if command := m.DefaultController().LatestCommand(receipt.TaskRef); command != nil && command.State == "failed" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	page, err := m.ListAdminTasks(ctx, TaskListOptions{Limit: 10})
	if err != nil || len(page.Data) != 1 || len(page.Data[0].Attention) == 0 || page.Data[0].Attention[0] != "命令失败" {
		t.Fatal(page, err)
	}
	devices, err := m.ListDeviceViews(ctx, DeviceListOptions{Health: "attention", Limit: 10})
	if err != nil || len(devices.Data) != 1 || devices.Data[0].LastError == "" {
		t.Fatal(devices, err)
	}
}

func ptr(v int) *int { return &v }
