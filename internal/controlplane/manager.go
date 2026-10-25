// Package controlplane owns the control service's in-memory device registry.
// It is deliberately process-local: AgentAPI remains the durable source of
// sessions and this package only supplies device routing and observability.
package controlplane

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/wwcchh0123/hackathonGO-2026/internal/agentapi"
	"github.com/wwcchh0123/hackathonGO-2026/internal/artifacts"
	"github.com/wwcchh0123/hackathonGO-2026/internal/device"
	"github.com/wwcchh0123/hackathonGO-2026/internal/session"
)

var ErrUnknownDevice = errors.New("设备未知或已过期")

type Options struct {
	// OnlineWindow is the period in which a touched device is shown online.
	// It describes control-plane reachability, not a hardware heartbeat.
	OnlineWindow time.Duration
	Session      session.Options
}

type runtime struct {
	id             string
	state          *device.State
	controller     *session.Controller
	firstSeen      time.Time
	lastSeen       time.Time
	lastSeenSource string
}

type Manager struct {
	ctx          context.Context
	provider     agentapi.Provider
	dataDir      string
	publicURL    string
	store        *artifacts.Store
	defaultID    string
	onlineWindow time.Duration
	session      session.Options
	mu           sync.RWMutex
	devices      map[string]*runtime
}

type TaskCounts struct {
	Total       int  `json:"total"`
	Running     int  `json:"running"`
	Idle        int  `json:"idle"`
	Rescheduled int  `json:"rescheduled"`
	Terminated  int  `json:"terminated"`
	Other       int  `json:"other"`
	HasMore     bool `json:"has_more"`
}

type DeviceOverview struct {
	DeviceID       string          `json:"device_id"`
	Default        bool            `json:"default"`
	FirstSeen      time.Time       `json:"first_seen"`
	LastSeen       *time.Time      `json:"last_seen,omitempty"`
	LastSeenSource string          `json:"last_seen_source,omitempty"`
	Online         bool            `json:"online"`
	Snapshot       device.Snapshot `json:"snapshot"`
	TaskCounts     TaskCounts      `json:"task_counts"`
	TasksError     string          `json:"tasks_error,omitempty"`
}

func New(ctx context.Context, provider agentapi.Provider, defaultID, dataDir, publicURL string, options ...Options) *Manager {
	var o Options
	if len(options) > 0 {
		o = options[0]
	}
	if o.OnlineWindow <= 0 {
		o.OnlineWindow = 90 * time.Second
	}
	if o.Session.DeviceID == "" {
		o.Session.DeviceID = defaultID
	}
	o.Session.IncludeUnowned = true
	m := &Manager{ctx: ctx, provider: provider, dataDir: dataDir, publicURL: publicURL, defaultID: defaultID, onlineWindow: o.OnlineWindow, session: o.Session, store: &artifacts.Store{Root: dataDir, PublicURL: publicURL}, devices: map[string]*runtime{}}
	_, _ = m.ensure(defaultID, "configured", o.Session)
	return m
}

func (m *Manager) Wait() {
	m.mu.RLock()
	runtimes := make([]*runtime, 0, len(m.devices))
	for _, r := range m.devices {
		runtimes = append(runtimes, r)
	}
	m.mu.RUnlock()
	for _, r := range runtimes {
		r.controller.Wait()
	}
}

func validDeviceID(id string) bool {
	return id != "" && utf8.RuneCountInString(id) <= 200 && !strings.ContainsAny(id, "\r\n/\\")
}

func (m *Manager) ensure(id string, source string, options session.Options) (*runtime, error) {
	if !validDeviceID(id) {
		return nil, ErrUnknownDevice
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.devices[id]; r != nil {
		if source != "" {
			r.lastSeen = time.Now().UTC()
			r.lastSeenSource = source
		}
		return r, nil
	}
	options.DeviceID = id
	state := device.NewState(id)
	c := session.New(m.ctx, m.provider, state, m.store, options)
	now := time.Now().UTC()
	r := &runtime{id: id, state: state, controller: c, firstSeen: now}
	if source != "configured" {
		r.lastSeen, r.lastSeenSource = now, source
	}
	m.devices[id] = r
	return r, nil
}

// Ensure discovers a device from a trusted control-plane request and returns
// its controller. The caller must authenticate the request before invoking it.
func (m *Manager) Ensure(id, source string) (*session.Controller, error) {
	o := m.session
	o.DeviceID = id
	o.IncludeUnowned = false
	r, err := m.ensure(id, source, o)
	if err != nil {
		return nil, err
	}
	return r.controller, nil
}

func (m *Manager) Controller(id string) (*session.Controller, bool) {
	m.mu.RLock()
	r := m.devices[id]
	m.mu.RUnlock()
	if r == nil {
		return nil, false
	}
	return r.controller, true
}

func (m *Manager) DefaultController() *session.Controller {
	c, _ := m.Controller(m.defaultID)
	return c
}

func (m *Manager) Touch(id, source string) error {
	_, err := m.Ensure(id, source)
	return err
}

func (m *Manager) Exists(id string) bool {
	m.mu.RLock()
	_, ok := m.devices[id]
	m.mu.RUnlock()
	return ok
}

func (m *Manager) Overview(ctx context.Context, id string) (DeviceOverview, error) {
	m.mu.RLock()
	r := m.devices[id]
	if r == nil {
		m.mu.RUnlock()
		return DeviceOverview{}, ErrUnknownDevice
	}
	snapshot := r.state.Get()
	first, last, source := r.firstSeen, r.lastSeen, r.lastSeenSource
	m.mu.RUnlock()

	o := DeviceOverview{DeviceID: id, Default: id == m.defaultID, FirstSeen: first, Snapshot: snapshot, LastSeenSource: source}
	if !last.IsZero() {
		lastCopy := last
		o.LastSeen = &lastCopy
		o.Online = time.Since(last) <= m.onlineWindow
	}
	page, err := r.controller.ListTasks(ctx, session.ListOptions{Limit: 50, IncludeArchived: true})
	if err != nil {
		o.TasksError = err.Error()
		return o, nil
	}
	o.TaskCounts.HasMore = page.HasMore
	for _, task := range page.Data {
		o.TaskCounts.Total++
		switch task.Status {
		case "running":
			o.TaskCounts.Running++
		case "idle":
			o.TaskCounts.Idle++
		case "rescheduling":
			o.TaskCounts.Rescheduled++
		case "terminated":
			o.TaskCounts.Terminated++
		default:
			o.TaskCounts.Other++
		}
	}
	return o, nil
}

func (m *Manager) ListDevices(ctx context.Context) ([]DeviceOverview, time.Time) {
	m.mu.RLock()
	ids := make([]string, 0, len(m.devices))
	for id := range m.devices {
		ids = append(ids, id)
	}
	m.mu.RUnlock()
	sort.Strings(ids)
	out := make([]DeviceOverview, len(ids))
	limit := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			select {
			case limit <- struct{}{}:
				defer func() { <-limit }()
			case <-ctx.Done():
				return
			}
			if overview, err := m.Overview(ctx, id); err == nil {
				out[i] = overview
			}
		}(i, id)
	}
	wg.Wait()
	filtered := out[:0]
	for _, overview := range out {
		if overview.DeviceID != "" {
			filtered = append(filtered, overview)
		}
	}
	return filtered, time.Now().UTC()
}

func (m *Manager) ListTasks(ctx context.Context, id string, options session.ListOptions) (session.TaskPage, error) {
	c, ok := m.Controller(id)
	if !ok {
		return session.TaskPage{}, ErrUnknownDevice
	}
	return c.ListTasks(ctx, options)
}

func (m *Manager) GetTask(ctx context.Context, id, ref string, options session.DetailOptions) (session.Detail, error) {
	c, ok := m.Controller(id)
	if !ok {
		return session.Detail{}, ErrUnknownDevice
	}
	return c.GetTask(ctx, ref, options)
}

func (m *Manager) Snapshot(id string) (device.Snapshot, error) {
	m.mu.RLock()
	r := m.devices[id]
	m.mu.RUnlock()
	if r == nil {
		return device.Snapshot{}, ErrUnknownDevice
	}
	return r.state.Get(), nil
}
