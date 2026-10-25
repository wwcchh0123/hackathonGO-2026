package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/wwcchh0123/hackathonGO-2026/internal/agentapi"
	"github.com/wwcchh0123/hackathonGO-2026/internal/events"
	"github.com/wwcchh0123/hackathonGO-2026/internal/progress"
)

func parseMessage(f events.Frame) Message { return progress.ReadMessage(f) }
func (c *Controller) summary(ctx context.Context, id string) (*Message, bool) {
	page, err := c.Provider.ListEvents(ctx, id, agentapi.EventQuery{Limit: 8, Order: "desc", Types: []string{"agent.message"}})
	if err != nil {
		return nil, true
	}
	for _, f := range page.Data {
		msg := parseMessage(f)
		if msg.Text != "" {
			msg.Text = truncate(msg.Text, 400)
			return &msg, false
		}
	}
	return nil, page.NextPage != ""
}
func taskCard(s agentapi.Session, ref string) Task {
	t := Task{Ref: ref, SessionID: s.ID, Title: s.Title, Status: s.Status, Runtime: s.Runtime, Archived: s.ArchivedAt != nil, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, CanContinue: true}
	if err := compatible(s); err != nil {
		t.CanContinue = false
		t.ContinueReason = err.Error()
	}
	return t
}

func (c *Controller) ownsSession(s agentapi.Session) bool {
	if c.deviceID == "" {
		return true
	}
	if s.Metadata["device_id"] == c.deviceID {
		return true
	}
	return c.includeUnowned && s.Metadata["device_id"] == ""
}

// listSessions applies the controller's device ownership boundary after the
// provider query. AgentAPI does not expose metadata filtering, so it walks a
// bounded number of provider pages until it has enough owned sessions. The
// returned cursor remains an upstream session cursor and therefore stays safe
// across refreshes and process restarts.
func (c *Controller) listSessions(ctx context.Context, o ListOptions) (agentapi.SessionPage, error) {
	if c.deviceID == "" {
		return c.Provider.ListSessions(ctx, agentapi.SessionQuery{Limit: o.Limit, Page: o.Page, AgentID: o.AgentID, Status: o.Status, IncludeArchived: o.IncludeArchived, CreatedAfter: o.CreatedAfter, CreatedBefore: o.CreatedBefore})
	}
	queryLimit := o.Limit * 2
	if queryLimit < 20 {
		queryLimit = 20
	}
	if queryLimit > 100 {
		queryLimit = 100
	}
	page := o.Page
	out := make([]agentapi.Session, 0, o.Limit)
	for range 16 {
		result, err := c.Provider.ListSessions(ctx, agentapi.SessionQuery{Limit: queryLimit, Page: page, AgentID: o.AgentID, Status: o.Status, IncludeArchived: o.IncludeArchived, CreatedAfter: o.CreatedAfter, CreatedBefore: o.CreatedBefore})
		if err != nil {
			return agentapi.SessionPage{}, err
		}
		for i, s := range result.Data {
			if c.ownsSession(s) {
				out = append(out, s)
				if len(out) == o.Limit {
					next := result.NextPage
					if next == "" {
						for _, later := range result.Data[i+1:] {
							if c.ownsSession(later) {
								next = result.Data[len(result.Data)-1].ID
								break
							}
						}
					}
					return agentapi.SessionPage{Data: out, NextPage: next}, nil
				}
			}
		}
		if result.NextPage == "" {
			return agentapi.SessionPage{Data: out}, nil
		}
		page = result.NextPage
	}
	return agentapi.SessionPage{Data: out, NextPage: page}, nil
}
func validateList(o *ListOptions) error {
	if o.Limit == 0 {
		o.Limit = 20
	}
	if o.Limit < 1 || o.Limit > 50 {
		return errors.New("limit 须为 1 至 50")
	}
	if o.Status != "" && !agentapi.KnownStatus(o.Status) {
		return errors.New("未知状态过滤")
	}
	for _, value := range []string{o.CreatedAfter, o.CreatedBefore} {
		if value != "" {
			if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
				return errors.New("时间须为 RFC3339 格式")
			}
		}
	}
	if len(o.Page) > 512 || len(o.AgentID) > 200 {
		return errors.New("查询参数过长")
	}
	return nil
}
func (c *Controller) ListTasks(ctx context.Context, o ListOptions) (TaskPage, error) {
	if err := validateList(&o); err != nil {
		return TaskPage{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.options.RequestTimeout)
	defer cancel()
	now := time.Now().UTC()
	page, err := c.listSessions(ctx, o)
	if err != nil {
		return TaskPage{}, err
	}
	out := TaskPage{Data: make([]Task, len(page.Data)), NextPage: page.NextPage, HasMore: page.NextPage != "", Sort: "created_at_desc", Scope: "current_agentapi_identity", QueriedAt: now, Filters: o}
	for i, s := range page.Data {
		c.mu.Lock()
		ref, err := c.referenceLocked(s)
		c.mu.Unlock()
		if err != nil {
			return TaskPage{}, err
		}
		out.Data[i] = taskCard(s, ref)
	}
	if !o.SkipSummary {
		var wg sync.WaitGroup
		limit := make(chan struct{}, 4)
		for i := range out.Data {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				select {
				case limit <- struct{}{}:
					defer func() { <-limit }()
				case <-ctx.Done():
					out.Data[i].SummaryUnavailable = true
					return
				}
				out.Data[i].Summary, out.Data[i].SummaryUnavailable = c.summary(ctx, out.Data[i].SessionID)
			}(i)
		}
		wg.Wait()
		for _, t := range out.Data {
			if t.SummaryUnavailable {
				out.Partial = true
			}
		}
	}
	return out, nil
}
func (c *Controller) GetTask(ctx context.Context, ref string, o DetailOptions) (Detail, error) {
	n := 30
	if o.EventsLimit != nil {
		n = *o.EventsLimit
	}
	if n < 0 || n > 100 || len(o.EventsPage) > 512 || len(o.FilesPage) > 512 {
		return Detail{}, errors.New("无效的事件数量或游标")
	}
	c.mu.Lock()
	t, err := c.recordLocked(ref)
	if err != nil {
		c.mu.Unlock()
		return Detail{}, err
	}
	key := o.CommandID
	if key == "" && len(t.order) > 0 {
		key = t.order[len(t.order)-1]
	}
	state := t.commands[key]
	unknown := state != nil && state.State == "unknown"
	c.mu.Unlock()
	if unknown {
		c.reconcile(ctx, ref, key)
	}
	ctx, cancel := context.WithTimeout(ctx, c.options.RequestTimeout)
	defer cancel()
	c.mu.Lock()
	id := t.id
	d := Detail{Task: Task{Ref: ref, SessionID: id, Title: truncate(t.prompt, 100), Status: "creating", CreatedAt: t.created}, QueriedAt: time.Now().UTC(), StopReason: "unknown", Messages: []Message{}, Events: []json.RawMessage{}, Files: agentapi.FilePage{Data: []agentapi.FileInfo{}}, ResultURL: t.resultURL}
	if key != "" {
		if s := t.commands[key]; s != nil {
			copy := *s
			d.Command = &copy
		} else {
			c.mu.Unlock()
			return Detail{}, errors.New("命令记录未知或已过期")
		}
	}
	c.mu.Unlock()
	if d.ResultURL != "" && !c.store.Available(d.ResultURL) {
		c.mu.Lock()
		if t.resultURL == d.ResultURL {
			t.resultURL = ""
		}
		c.mu.Unlock()
		d.ResultURL = ""
	}
	if id == "" {
		if d.Command != nil {
			if d.Command.State == "unknown" || d.Command.State == "failed" {
				d.Status = d.Command.State
			}
		}
		return d, nil
	}
	s, err := c.status(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	d.Task = taskCard(s, ref)
	d.Summary, d.SummaryUnavailable = c.summary(ctx, id)
	// Control-only tail is independent from the historical page requested by the
	// caller. An older page can never replace the current stop reason.
	control, err := c.Provider.ListEvents(ctx, id, agentapi.EventQuery{Limit: 64, Order: "desc", Types: []string{"user.message", "agent.message", "session.status_idle", "session.status_running", "session.status_rescheduled", "session.status_terminated"}})
	if err != nil {
		return Detail{}, err
	}
	d.StopReason = stopReason(s.Status, control.Data)
	for _, f := range control.Data {
		if (f.Type == "agent.message" || f.Type == "user.message") && len(d.Messages) < 12 {
			msg := parseMessage(f)
			if msg.Text != "" {
				msg.Text = truncate(msg.Text, 8000)
				d.Messages = append(d.Messages, msg)
			}
		}
	}

	if n > 0 {
		page, err := c.Provider.ListEvents(ctx, id, agentapi.EventQuery{Limit: n, Page: o.EventsPage, Order: "desc"})
		if err != nil {
			return Detail{}, err
		}
		for i := len(page.Data) - 1; i >= 0; i-- {
			d.Events = append(d.Events, page.Data[i].Data)
		}
		d.EventsNextPage = page.NextPage
		d.EventsHasMore = page.NextPage != ""
	}
	d.Files, err = c.Provider.ListFiles(ctx, id, 50, o.FilesPage)
	if err != nil {
		d.Files = agentapi.FilePage{Data: []agentapi.FileInfo{}}
		d.FilesUnavailable = true
	}
	return d, nil
}

// reconcile reads bounded evidence only. It never resends or creates a Session.
func (c *Controller) reconcile(ctx context.Context, ref, key string) bool {
	c.mu.Lock()
	t := c.tasks[ref]
	if t == nil {
		c.mu.Unlock()
		return false
	}
	entry := t.commands[key]
	if entry == nil {
		c.mu.Unlock()
		return false
	}
	snapshot := *entry
	id := t.id
	c.mu.Unlock()
	if snapshot.State != "unknown" {
		return true
	}
	ctx, cancel := context.WithTimeout(ctx, c.options.ReconcileTimeout)
	defer cancel()
	if snapshot.Phase == "create" {
		page := ""
		for range 3 {
			list, err := c.Provider.ListSessions(ctx, agentapi.SessionQuery{Limit: 100, Page: page, CreatedAfter: snapshot.AcceptedAt.Add(-time.Minute).UTC().Format(time.RFC3339Nano), IncludeArchived: true})
			if err != nil {
				return false
			}
			for _, s := range list.Data {
				if s.Metadata["client_request_id"] == key && s.Metadata["source"] == "linxi-control" {
					c.mu.Lock()
					t.id = s.ID
					c.sessions[s.ID] = ref
					c.mu.Unlock()
					c.updateCommand(ref, key, "failed", "created", errors.New("已找回创建的会话；首条需求尚未发送，请明确发送到此任务"), "")
					return true
				}
			}
			if list.NextPage == "" {
				return false
			}
			page = list.NextPage
		}
		return false
	}
	if id == "" {
		return false
	}
	page := ""
	for range 4 {
		list, err := c.Provider.ListEvents(ctx, id, agentapi.EventQuery{Limit: 100, Page: page, Order: "desc", Types: []string{"user.message"}})
		if err != nil {
			return false
		}
		for _, f := range list.Data {
			var e struct {
				Request string `json:"client_request_id"`
				Payload struct {
					Request string `json:"client_request_id"`
				} `json:"payload"`
			}
			if json.Unmarshal(f.Data, &e) == nil && (e.Request == key || e.Payload.Request == key) {
				c.updateCommand(ref, key, "confirmed", "send", nil, f.ID)
				return true
			}
		}
		if list.NextPage == "" {
			return false
		}
		page = list.NextPage
	}
	return false
}
func (c *Controller) SessionFor(ctx context.Context, ref string) (agentapi.Session, error) {
	c.mu.Lock()
	t, err := c.recordLocked(ref)
	id := ""
	if t != nil {
		id = t.id
	}
	c.mu.Unlock()
	if err != nil {
		return agentapi.Session{}, err
	}
	if id == "" {
		return agentapi.Session{}, fmt.Errorf("云端会话尚未创建")
	}
	return c.status(ctx, id)
}

func stopReason(status string, tail []events.Frame) string {
	if status != "idle" {
		return "unknown"
	}
	reason := "unknown"
	seen := false
	for _, f := range tail {
		if f.Type == "user.message" {
			return reason
		}
		if f.Type == "session.status_idle" && !seen {
			seen = true
			var e struct {
				Stop struct {
					Type string `json:"type"`
				} `json:"stop_reason"`
			}
			if json.Unmarshal(f.Data, &e) == nil && e.Stop.Type != "" {
				reason = e.Stop.Type
			}
		}
	}
	return "unknown"
}
