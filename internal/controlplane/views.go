package controlplane

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wwcchh0123/hackathonGO-2026/internal/session"
)

type DeviceListOptions struct {
	Query  string `json:"query,omitempty"`
	Health string `json:"health,omitempty"`
	Sort   string `json:"sort,omitempty"`
	Limit  int    `json:"limit,omitempty"`
	Page   string `json:"page,omitempty"`
}

type DevicePage struct {
	Data      []DeviceOverview  `json:"data"`
	NextPage  string            `json:"next_page,omitempty"`
	HasMore   bool              `json:"has_more"`
	Sort      string            `json:"sort"`
	QueriedAt time.Time         `json:"queried_at"`
	Filters   DeviceListOptions `json:"filters"`
}

type AdminTask struct {
	session.Task
	DeviceID     string                 `json:"device_id"`
	DeviceOnline bool                   `json:"device_online"`
	Command      *session.CommandStatus `json:"command,omitempty"`
	Attention    []string               `json:"attention,omitempty"`
}

type TaskListOptions struct {
	Query    string `json:"query,omitempty"`
	DeviceID string `json:"device_id,omitempty"`
	Status   string `json:"status,omitempty"`
	Limit    int    `json:"limit,omitempty"`
	Page     string `json:"page,omitempty"`
}

type AdminTaskPage struct {
	Data      []AdminTask     `json:"data"`
	NextPage  string          `json:"next_page,omitempty"`
	HasMore   bool            `json:"has_more"`
	Sort      string          `json:"sort"`
	QueriedAt time.Time       `json:"queried_at"`
	Filters   TaskListOptions `json:"filters"`
	Partial   bool            `json:"partial"`
}

type Dashboard struct {
	QueriedAt        time.Time        `json:"queried_at"`
	DeviceCount      int              `json:"device_count"`
	OnlineCount      int              `json:"online_count"`
	AttentionDevices int              `json:"attention_devices"`
	RunningTasks     int              `json:"running_tasks"`
	AttentionTasks   int              `json:"attention_tasks"`
	Devices          []DeviceOverview `json:"devices"`
	Tasks            []AdminTask      `json:"tasks"`
	Partial          bool             `json:"partial"`
}

func validateDeviceList(o *DeviceListOptions) error {
	if o.Limit == 0 {
		o.Limit = 20
	}
	if o.Limit < 1 || o.Limit > 100 {
		return errors.New("limit 须为 1 至 100")
	}
	if o.Health != "" && o.Health != "online" && o.Health != "offline" && o.Health != "unseen" && o.Health != "attention" {
		return errors.New("未知设备健康状态")
	}
	if o.Sort == "" {
		o.Sort = "attention"
	}
	if o.Sort != "attention" && o.Sort != "last_seen" && o.Sort != "device_id" {
		return errors.New("未知设备排序")
	}
	if len(o.Query) > 200 || len(o.Page) > 32 {
		return errors.New("查询参数过长")
	}
	return nil
}

func validateTaskList(o *TaskListOptions) error {
	if o.Limit == 0 {
		o.Limit = 20
	}
	if o.Limit < 1 || o.Limit > 100 {
		return errors.New("limit 须为 1 至 100")
	}
	if o.Status != "" && o.Status != "idle" && o.Status != "running" && o.Status != "rescheduling" && o.Status != "terminated" {
		return errors.New("未知任务状态过滤")
	}
	if len(o.Query) > 200 || len(o.DeviceID) > 200 || len(o.Page) > 32 {
		return errors.New("查询参数过长")
	}
	return nil
}

func pageOffset(raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 || n > 1000000 {
		return 0, errors.New("无效分页游标")
	}
	return n, nil
}

func deviceHealth(d DeviceOverview) string {
	if len(d.Attention) > 0 {
		return "attention"
	}
	if d.LastSeen == nil {
		return "unseen"
	}
	if !d.Online {
		return "offline"
	}
	return "online"
}

func (m *Manager) buildAdminTask(ctx context.Context, id string, online bool, task session.Task) (AdminTask, error) {
	c, ok := m.Controller(id)
	if !ok {
		return AdminTask{}, ErrUnknownDevice
	}
	item := AdminTask{Task: task, DeviceID: id, DeviceOnline: online, Command: c.LatestCommand(task.Ref)}
	if item.Command != nil {
		switch item.Command.State {
		case "unknown":
			item.Attention = append(item.Attention, "命令结果未知")
		case "failed":
			item.Attention = append(item.Attention, "命令失败")
		}
	}
	if task.Status == "terminated" {
		item.Attention = append(item.Attention, "任务已终止")
	}
	if task.SummaryUnavailable {
		item.Attention = append(item.Attention, "摘要读取失败")
	}
	if !online && task.Status == "running" {
		item.Attention = append(item.Attention, "设备离线但任务仍运行")
	}
	return item, nil
}

func taskMatch(item AdminTask, query string) bool {
	if query == "" {
		return true
	}
	needle := strings.ToLower(strings.TrimSpace(query))
	parts := []string{item.DeviceID, item.Ref, item.SessionID, item.Title}
	if item.Summary != nil {
		parts = append(parts, item.Summary.Text)
	}
	for _, part := range parts {
		if strings.Contains(strings.ToLower(part), needle) {
			return true
		}
	}
	return false
}

func (m *Manager) deviceViews(ctx context.Context) ([]DeviceOverview, bool) {
	devices, _ := m.ListDevices(ctx)
	partial := false
	for i := range devices {
		if devices[i].TasksError != "" {
			partial = true
		}
		if devices[i].TaskCounts.HasMore {
			partial = true
		}
	}
	return devices, partial
}

func (m *Manager) ListDeviceViews(ctx context.Context, o DeviceListOptions) (DevicePage, error) {
	if err := validateDeviceList(&o); err != nil {
		return DevicePage{}, err
	}
	offset, err := pageOffset(o.Page)
	if err != nil {
		return DevicePage{}, err
	}
	devices, _ := m.ListDevices(ctx)
	query := strings.ToLower(strings.TrimSpace(o.Query))
	filtered := devices[:0]
	for _, d := range devices {
		if query != "" && !strings.Contains(strings.ToLower(d.DeviceID), query) {
			continue
		}
		if o.Health != "" && deviceHealth(d) != o.Health {
			continue
		}
		filtered = append(filtered, d)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		a, b := filtered[i], filtered[j]
		switch o.Sort {
		case "last_seen":
			if a.LastSeen == nil || b.LastSeen == nil {
				return a.LastSeen != nil
			}
			if !a.LastSeen.Equal(*b.LastSeen) {
				return a.LastSeen.After(*b.LastSeen)
			}
		case "device_id":
			return a.DeviceID < b.DeviceID
		default:
			if len(a.Attention) != len(b.Attention) {
				return len(a.Attention) > len(b.Attention)
			}
			if a.Online != b.Online {
				return !a.Online
			}
		}
		return a.DeviceID < b.DeviceID
	})
	end := offset + o.Limit
	if offset > len(filtered) {
		offset = len(filtered)
	}
	if end > len(filtered) {
		end = len(filtered)
	}
	page := DevicePage{Data: make([]DeviceOverview, 0), Sort: o.Sort, QueriedAt: time.Now().UTC(), Filters: o}
	page.Data = append(page.Data, filtered[offset:end]...)
	page.HasMore = end < len(filtered)
	if page.HasMore {
		page.NextPage = strconv.Itoa(end)
	}
	return page, nil
}

func (m *Manager) ListAdminTasks(ctx context.Context, o TaskListOptions) (AdminTaskPage, error) {
	if err := validateTaskList(&o); err != nil {
		return AdminTaskPage{}, err
	}
	offset, err := pageOffset(o.Page)
	if err != nil {
		return AdminTaskPage{}, err
	}
	m.mu.RLock()
	ids := make([]string, 0, len(m.devices))
	for id := range m.devices {
		if o.DeviceID == "" || id == o.DeviceID {
			ids = append(ids, id)
		}
	}
	m.mu.RUnlock()
	sort.Strings(ids)
	all := make([]AdminTask, 0)
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	partial := false
	for _, id := range ids {
		id := id
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				mu.Lock()
				partial = true
				mu.Unlock()
				return
			}
			c, ok := m.Controller(id)
			if !ok {
				return
			}
			page, err := c.ListTasks(ctx, session.ListOptions{Limit: 50, IncludeArchived: true})
			if err != nil {
				mu.Lock()
				partial = true
				mu.Unlock()
				return
			}
			m.mu.RLock()
			r := m.devices[id]
			last := r.lastSeen
			m.mu.RUnlock()
			online := !last.IsZero() && time.Since(last) <= m.onlineWindow
			local := make([]AdminTask, 0, len(page.Data))
			for _, task := range page.Data {
				item, buildErr := m.buildAdminTask(ctx, id, online, task)
				if buildErr == nil && (o.Status == "" || task.Status == o.Status) && taskMatch(item, o.Query) {
					local = append(local, item)
				}
			}
			mu.Lock()
			all = append(all, local...)
			if page.HasMore {
				partial = true
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	sort.SliceStable(all, func(i, j int) bool {
		if len(all[i].Attention) != len(all[j].Attention) {
			return len(all[i].Attention) > len(all[j].Attention)
		}
		return all[i].UpdatedAt.After(all[j].UpdatedAt)
	})
	end := offset + o.Limit
	if offset > len(all) {
		offset = len(all)
	}
	if end > len(all) {
		end = len(all)
	}
	result := AdminTaskPage{Data: make([]AdminTask, 0), Sort: "attention_updated_desc", QueriedAt: time.Now().UTC(), Filters: o, Partial: partial}
	result.Data = append(result.Data, all[offset:end]...)
	result.HasMore = end < len(all) || partial
	if result.HasMore {
		result.NextPage = strconv.Itoa(end)
	}
	return result, nil
}

func (m *Manager) Dashboard(ctx context.Context) (Dashboard, error) {
	return m.dashboard(ctx, "")
}

func (m *Manager) DashboardQuery(ctx context.Context, query string) (Dashboard, error) {
	return m.dashboard(ctx, query)
}

func (m *Manager) dashboard(ctx context.Context, query string) (Dashboard, error) {
	devices, partial := m.deviceViews(ctx)
	tasks, err := m.ListAdminTasks(ctx, TaskListOptions{Query: query, Limit: 100})
	if err != nil {
		return Dashboard{}, err
	}
	d := Dashboard{QueriedAt: time.Now().UTC(), Devices: make([]DeviceOverview, 0), Tasks: make([]AdminTask, 0), Partial: partial || tasks.Partial}
	for _, device := range devices {
		d.DeviceCount++
		if device.Online {
			d.OnlineCount++
		}
		if len(device.Attention) > 0 {
			d.AttentionDevices++
		}
		d.RunningTasks += device.TaskCounts.Running
	}
	for _, task := range tasks.Data {
		if len(task.Attention) > 0 {
			d.AttentionTasks++
		}
		if len(task.Attention) > 0 || query != "" {
			d.Tasks = append(d.Tasks, task)
		}
	}
	matchingDevices := map[string]bool{}
	for _, task := range tasks.Data {
		matchingDevices[task.DeviceID] = true
	}
	for _, device := range devices {
		matchesQuery := query == "" || strings.Contains(strings.ToLower(device.DeviceID), strings.ToLower(query)) || matchingDevices[device.DeviceID]
		if (len(device.Attention) > 0 || query != "") && matchesQuery {
			d.Devices = append(d.Devices, device)
		}
	}
	return d, nil
}
