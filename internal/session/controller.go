// Package session is the sole programming controller. It owns bounded volatile
// references and commands; AgentAPI owns durable Sessions and execution.
package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/wwcchh0123/hackathonGO-2026/internal/agentapi"
	"github.com/wwcchh0123/hackathonGO-2026/internal/artifacts"
	"github.com/wwcchh0123/hackathonGO-2026/internal/device"
)

var ErrQueueFull = errors.New("操作太频繁，请稍后再试")
var ErrReference = errors.New("任务引用未知或已过期，请重新列出任务")
var ErrUnknown = errors.New("上次提交结果未知，请先查询核对；不会自动重发")

type Options struct {
	CreateTimeout, RequestTimeout, StopTimeout, ReconcileTimeout, PublishTimeout, PollInterval time.Duration
	ReferenceTTL                                                                               time.Duration
	MaxTasks                                                                                   int
	// DeviceID scopes cloud task discovery to one control-plane device. An empty
	// value keeps the historical all-sessions behavior used by unit tests and
	// the single-controller compatibility handler.
	DeviceID       string
	IncludeUnowned bool
}

func defaults(o Options) Options {
	values := []*time.Duration{&o.CreateTimeout, &o.RequestTimeout, &o.StopTimeout, &o.ReconcileTimeout, &o.PublishTimeout, &o.PollInterval, &o.ReferenceTTL}
	for i, d := range []time.Duration{30 * time.Second, 10 * time.Second, 60 * time.Second, 10 * time.Second, 30 * time.Second, 100 * time.Millisecond, 30 * time.Minute} {
		if *values[i] <= 0 {
			*values[i] = d
		}
	}
	if o.MaxTasks <= 0 {
		o.MaxTasks = 256
	}
	return o
}

type taskRecord struct {
	ref, id, prompt, parent, creation string
	expires, created                  time.Time
	commands                          map[string]*CommandStatus
	order                             []string
	pending                           int
	observers                         int
	generation                        uint64
	resultURL                         string
	observedSequence                  int64
	observedIDs                       map[string]bool
	observedOrder                     []string
}
type dedupEntry struct {
	receipt Receipt
	expires time.Time
}
type Controller struct {
	Provider       agentapi.Provider
	State          *device.State
	store          *artifacts.Store
	ctx            context.Context
	queue          chan Command
	wg             sync.WaitGroup
	options        Options
	mu             sync.Mutex
	tasks          map[string]*taskRecord
	sessions       map[string]string
	targets        map[string]string
	recent         map[string]dedupEntry
	observing      chan struct{}
	previewing     chan struct{}
	viewGeneration uint64
	deviceID       string
	includeUnowned bool
}

func New(ctx context.Context, p agentapi.Provider, state *device.State, store *artifacts.Store, options ...Options) *Controller {
	var o Options
	if len(options) > 0 {
		o = options[0]
	}
	o = defaults(o)
	c := &Controller{Provider: p, State: state, store: store, ctx: ctx, queue: make(chan Command, 32), options: o, tasks: map[string]*taskRecord{}, sessions: map[string]string{}, targets: map[string]string{}, recent: map[string]dedupEntry{}, observing: make(chan struct{}, 4), previewing: make(chan struct{}, 2), deviceID: o.DeviceID, includeUnowned: o.IncludeUnowned}
	c.wg.Add(1)
	go c.work()
	return c
}
func (c *Controller) Wait() { c.wg.Wait() }

// LatestCommand returns a copy of the most recent local command for a task.
// It is used by the read-only control center without forcing a cloud detail
// query or changing the selected device view.
func (c *Controller) LatestCommand(ref string) *CommandStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.tasks[ref]
	if t == nil || len(t.order) == 0 {
		return nil
	}
	status := t.commands[t.order[len(t.order)-1]]
	if status == nil {
		return nil
	}
	copy := *status
	return &copy
}
func (c *Controller) pruneLocked(now time.Time) {
	for ref, t := range c.tasks {
		if t.pending == 0 && t.observers == 0 && now.After(t.expires) {
			delete(c.tasks, ref)
			if c.sessions[t.id] == ref {
				delete(c.sessions, t.id)
			}
		}
	}
	for key, ref := range c.targets {
		if c.tasks[ref] == nil {
			delete(c.targets, key)
		}
	}
	for key, e := range c.recent {
		if !now.Before(e.expires) {
			delete(c.recent, key)
		}
	}
}
func (c *Controller) recordLocked(ref string) (*taskRecord, error) {
	t := c.tasks[ref]
	if t == nil || (t.observers == 0 && time.Now().After(t.expires)) {
		return nil, ErrReference
	}
	t.expires = time.Now().Add(c.options.ReferenceTTL)
	return t, nil
}
func (c *Controller) referenceLocked(s agentapi.Session) (string, error) {
	now := time.Now()
	c.pruneLocked(now)
	if ref := c.sessions[s.ID]; ref != "" {
		if t := c.tasks[ref]; t != nil {
			t.expires = now.Add(c.options.ReferenceTTL)
			return ref, nil
		}
	}
	if len(c.tasks) >= c.options.MaxTasks {
		return "", errors.New("任务引用缓存已满，请稍后重试")
	}
	ref := "task_" + agentapi.ID()
	c.tasks[ref] = &taskRecord{ref: ref, id: s.ID, created: s.CreatedAt, expires: now.Add(c.options.ReferenceTTL), commands: map[string]*CommandStatus{}}
	c.sessions[s.ID] = ref
	return ref, nil
}
func (c *Controller) Submit(cmd Command) (Receipt, error) {
	cmd.Prompt = strings.TrimSpace(cmd.Prompt)
	if cmd.Action != "start" && cmd.Action != "message" {
		return Receipt{}, errors.New("未知指令")
	}
	if cmd.Prompt == "" || utf8.RuneCountInString(cmd.Prompt) > 2000 {
		return Receipt{}, errors.New("需求须为一至两千字")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ctx.Err() != nil {
		return Receipt{}, errors.New("服务正在关闭")
	}
	now := time.Now()
	c.pruneLocked(now)
	if cmd.Action == "message" && cmd.TaskRef == "" {
		if cmd.ConversationID == "" {
			return Receipt{}, errors.New("请明确选择任务")
		}
		cmd.TaskRef = c.targets[cmd.ConversationID]
	}
	if cmd.Action == "message" {
		if _, err := c.recordLocked(cmd.TaskRef); err != nil {
			return Receipt{}, err
		}
	}
	if cmd.ParentRef != "" {
		if cmd.Action != "start" {
			return Receipt{}, errors.New("仅新任务可指定接力来源")
		}
		if _, err := c.recordLocked(cmd.ParentRef); err != nil {
			return Receipt{}, err
		}
	}
	key := cmd.ConversationID + "\x00" + cmd.Action + "\x00" + cmd.TaskRef + "\x00" + cmd.ParentRef + "\x00" + cmd.Prompt
	if cmd.ConversationID != "" {
		if previous, ok := c.recent[key]; ok {
			return previous.receipt, nil
		}
	}
	if len(c.queue) >= cap(c.queue) {
		return Receipt{}, ErrQueueFull
	}
	if cmd.Action == "start" {
		if len(c.tasks) >= c.options.MaxTasks {
			return Receipt{}, errors.New("任务引用缓存已满")
		}
		cmd.TaskRef = "task_" + agentapi.ID()
		c.tasks[cmd.TaskRef] = &taskRecord{ref: cmd.TaskRef, prompt: cmd.Prompt, parent: cmd.ParentRef, created: now, expires: now.Add(c.options.ReferenceTTL), commands: map[string]*CommandStatus{}}
	}
	t := c.tasks[cmd.TaskRef]
	if len(t.order) >= 32 {
		old := t.commands[t.order[0]]
		if old.State == "queued" || old.State == "submitting" || old.State == "unknown" {
			return Receipt{}, errors.New("该任务待核对命令过多")
		}
		delete(t.commands, t.order[0])
		t.order = t.order[1:]
	}
	cmd.ID = "cmd_" + agentapi.ID()
	t.commands[cmd.ID] = &CommandStatus{ID: cmd.ID, State: "queued", Phase: "queued", AcceptedAt: now, UpdatedAt: now}
	t.order = append(t.order, cmd.ID)
	t.pending++
	t.expires = now.Add(c.options.ReferenceTTL)
	c.queue <- cmd
	if cmd.ConversationID != "" {
		if _, exists := c.targets[cmd.ConversationID]; !exists && len(c.targets) >= 1024 {
			for key := range c.targets {
				delete(c.targets, key)
				break
			}
		}
		c.targets[cmd.ConversationID] = cmd.TaskRef
	}
	receipt := Receipt{true, "已受理，请查询任务确认提交和执行结果", cmd.TaskRef, cmd.ID}
	slog.Info("command accepted", "command_id", cmd.ID, "task_ref", cmd.TaskRef, "source", cmd.Source)
	if cmd.ConversationID != "" {
		if len(c.recent) >= 1024 {
			for k := range c.recent {
				delete(c.recent, k)
				break
			}
		}
		c.recent[key] = dedupEntry{receipt, now.Add(5 * time.Second)}
	}
	return receipt, nil
}
func (c *Controller) updateCommand(ref, id, state, phase string, err error, event string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.tasks[ref]
	if t == nil {
		return
	}
	s := t.commands[id]
	if s == nil {
		return
	}
	s.State = state
	s.Phase = phase
	s.UpdatedAt = time.Now()
	s.Error = ""
	if err != nil {
		s.Error = err.Error()
	}
	if event != "" {
		s.EventID = event
	}
	if c.State.Get().TaskRef == ref && len(t.order) > 0 && t.order[len(t.order)-1] == id {
		c.State.Update(func(v *device.Snapshot) { v.CommandID = id; v.CommandState = state; v.Error = s.Error })
	}
}
func (c *Controller) work() {
	defer c.wg.Done()
	for {
		select {
		case <-c.ctx.Done():
			return
		case cmd := <-c.queue:
			if c.ctx.Err() != nil {
				return
			}
			err := c.execute(c.ctx, cmd)
			if err != nil {
				slog.Warn("command failed", "command_id", cmd.ID, "task_ref", cmd.TaskRef, "error", err)
				c.mu.Lock()
				s := c.tasks[cmd.TaskRef].commands[cmd.ID]
				unknown := s.State == "unknown"
				phase := s.Phase
				c.mu.Unlock()
				if !unknown {
					c.updateCommand(cmd.TaskRef, cmd.ID, "failed", phase, err, "")
				}
			}
			c.mu.Lock()
			if t := c.tasks[cmd.TaskRef]; t != nil {
				t.pending--
			}
			c.mu.Unlock()
		}
	}
}
func (c *Controller) status(ctx context.Context, id string) (agentapi.Session, error) {
	ctx, cancel := context.WithTimeout(ctx, c.options.RequestTimeout)
	defer cancel()
	s, err := c.Provider.Status(ctx, id)
	if err == nil && !agentapi.KnownStatus(s.Status) {
		err = errors.New("AgentAPI 返回未知会话状态")
	}
	return s, err
}
func compatible(s agentapi.Session) error {
	if s.ArchivedAt != nil {
		return errors.New("任务已归档，不能直接发送")
	}
	if s.Status == "terminated" {
		return errors.New("原运行环境已终止，请明确新建或接力任务")
	}
	if s.Runtime != "claudecode" {
		return errors.New("该任务运行时不兼容，仅支持 claudecode")
	}
	return nil
}
func (c *Controller) stop(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, c.options.StopTimeout)
	defer cancel()
	s, err := c.status(ctx, id)
	if err != nil {
		return err
	}
	if err = compatible(s); err != nil {
		return err
	}
	if s.Status == "idle" {
		return nil
	}
	err = c.Provider.Interrupt(ctx, id)
	if err != nil && !agentapi.Uncertain(err) {
		return err
	}
	ticker := time.NewTicker(c.options.PollInterval)
	defer ticker.Stop()
	for {
		s, err = c.status(ctx, id)
		if err == nil {
			if err = compatible(s); err != nil {
				return err
			}
			if s.Status == "idle" {
				return nil
			}
		} else if !agentapi.Retryable(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("等待运行时停止: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}
func (c *Controller) execute(ctx context.Context, cmd Command) error {
	c.mu.Lock()
	t := c.tasks[cmd.TaskRef]
	id := t.id
	var unknown []string
	for _, key := range t.order {
		if key == cmd.ID {
			break
		}
		if t.commands[key].State == "unknown" {
			unknown = append(unknown, key)
		}
	}
	c.mu.Unlock()
	for _, key := range unknown {
		if !c.reconcile(ctx, cmd.TaskRef, key) {
			return ErrUnknown
		}
	}
	c.updateCommand(cmd.TaskRef, cmd.ID, "submitting", "checking", nil, "")
	text := cmd.Prompt
	if cmd.Action == "start" {
		if cmd.ParentRef != "" {
			zero := 0
			d, err := c.GetTask(ctx, cmd.ParentRef, DetailOptions{EventsLimit: &zero})
			if err != nil {
				return fmt.Errorf("读取接力来源: %w", err)
			}
			id = d.SessionID
			if id == "" {
				return errors.New("接力来源尚未创建云端会话")
			}
			text += "\n\n用户明确要求接力以下历史任务。这里只包含已读取的信息，不保证未提交文件仍存在：\n" + d.Title
			for _, msg := range d.Messages {
				text += "\n" + msg.Text
			}
			for _, f := range d.Files.Data {
				text += "\n已导出文件: " + f.Path
			}
			if len([]rune(text)) > 12000 {
				text = string([]rune(text)[:12000])
			}
		}
		metadata := map[string]string{"source": "linxi-control", "device_id": c.State.Get().DeviceID, "client_request_id": cmd.ID}
		if cmd.ParentRef != "" {
			metadata["parent_session_id"] = id
		}
		c.updateCommand(cmd.TaskRef, cmd.ID, "submitting", "create", nil, "")
		createCtx, cancel := context.WithTimeout(ctx, c.options.CreateTimeout)
		created, err := c.Provider.Create(createCtx, agentapi.NewSession{Title: truncate(cmd.Prompt, 100), Metadata: metadata})
		cancel()
		if err != nil || created.ID == "" {
			if err == nil {
				err = &agentapi.Error{Operation: "POST session", Ambiguous: true}
			}
			if agentapi.Uncertain(err) {
				c.mu.Lock()
				t.creation = cmd.ID
				c.mu.Unlock()
				c.updateCommand(cmd.TaskRef, cmd.ID, "unknown", "create", err, "")
			}
			return err
		}
		id = created.ID
		c.mu.Lock()
		t.id = id
		c.sessions[id] = t.ref
		if c.State.Get().TaskRef == t.ref {
			c.State.Update(func(s *device.Snapshot) { s.SessionID = id; s.Status = created.Status; s.StopReason = "unknown" })
		}
		c.mu.Unlock()
	} else {
		c.mu.Lock()
		id = t.id
		c.mu.Unlock()
		if id == "" {
			return errors.New("任务尚未创建成功，请查询原创建命令")
		}
		c.updateCommand(cmd.TaskRef, cmd.ID, "submitting", "interrupt", nil, "")
		if err := c.stop(ctx, id); err != nil {
			return err
		}
	}
	c.updateCommand(cmd.TaskRef, cmd.ID, "submitting", "send", nil, "")
	// Validate even a newly created Session; creation alone does not prove access
	// or that the configured deployment resolves a compatible runtime.
	s, err := c.status(ctx, id)
	if err != nil {
		return err
	}
	if err = compatible(s); err != nil {
		return err
	}
	c.mu.Lock()
	t.generation++
	t.prompt = cmd.Prompt
	t.resultURL = ""
	if c.State.Get().TaskRef == t.ref {
		c.State.Update(func(s *device.Snapshot) { s.ResultURL = ""; s.Prompt = cmd.Prompt })
	}
	c.mu.Unlock()
	sendCtx, cancel := context.WithTimeout(ctx, c.options.RequestTimeout)
	accepted, err := c.Provider.SendMessage(sendCtx, id, agentapi.Message{Text: text, CommandID: cmd.ID})
	cancel()
	if err == nil && accepted.EventID != "" {
		c.updateCommand(cmd.TaskRef, cmd.ID, "confirmed", "send", nil, accepted.EventID)
		return nil
	}
	if err == nil {
		err = &agentapi.Error{Operation: "POST message acknowledgement", Ambiguous: true}
	}
	if agentapi.Uncertain(err) {
		c.updateCommand(cmd.TaskRef, cmd.ID, "unknown", "send", err, "")
		if c.reconcile(ctx, cmd.TaskRef, cmd.ID) {
			return nil
		}
	}
	return err
}
func truncate(text string, n int) string {
	r := []rune(text)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return text
}
