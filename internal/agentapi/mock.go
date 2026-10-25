package agentapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wwcchh0123/hackathonGO-2026/internal/events"
	"github.com/wwcchh0123/hackathonGO-2026/internal/runtimeprofile"
)

type mockSession struct {
	info           Session
	status, prompt string
	cancel         context.CancelFunc
	done           chan struct{}
	ready          bool
}
type Mock struct {
	mu         sync.Mutex
	sessions   map[string]*mockSession
	hub        *events.Hub
	ctx        context.Context
	Step       time.Duration
	sequenceMu sync.Mutex
	sequences  map[string]int64
	profile    runtimeprofile.Profile
}

func NewMock(ctx context.Context) *Mock {
	return NewMockForProfile(ctx, runtimeprofile.MockRegistry().Profiles[0])
}

func NewMockForProfile(ctx context.Context, profile runtimeprofile.Profile) *Mock {
	return &Mock{sessions: map[string]*mockSession{}, hub: events.NewHub(), ctx: ctx, Step: 900 * time.Millisecond, sequences: map[string]int64{}, profile: profile}
}
func ID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func (m *Mock) emit(id, kind string, extra map[string]any) string {
	m.sequenceMu.Lock()
	defer m.sequenceMu.Unlock()
	if extra == nil {
		extra = map[string]any{}
	}
	extra["id"] = "evt_" + ID()
	extra["type"] = kind
	extra["created_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	m.sequences[id]++
	extra["sequence_number"] = m.sequences[id]
	b, _ := json.Marshal(extra)
	m.hub.Append(id, events.Frame{ID: extra["id"].(string), Type: kind, Data: b})
	return extra["id"].(string)
}
func (m *Mock) Create(ctx context.Context, input ...NewSession) (Session, error) {
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}
	id := "ses_mock_" + ID()
	m.mu.Lock()
	if len(m.sessions) >= 256 {
		m.mu.Unlock()
		return Session{}, fmt.Errorf("mock session limit reached")
	}
	info := Session{ID: id, Status: "idle", Runtime: m.profile.Runtime, AgentID: m.profile.AgentID, AgentVersion: m.profile.AgentVersion, EnvironmentID: m.profile.EnvironmentID, Model: Model{ID: m.profile.DefaultModel.ID, Effort: m.profile.DefaultModel.Effort}, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if len(input) > 0 {
		info.Title = input[0].Title
		info.Metadata = input[0].Metadata
		if input[0].Model.ID != "" {
			info.Model = input[0].Model
		}
	}
	m.sessions[id] = &mockSession{status: "idle", info: info}
	m.mu.Unlock()
	m.emit(id, "session.created", nil)
	return info, nil
}
func (m *Mock) Status(ctx context.Context, id string) (Session, error) {
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[id]
	if s == nil {
		return Session{}, &Error{Operation: "GET session", StatusCode: 404}
	}
	info := s.info
	info.Status = s.status
	return info, nil
}
func (m *Mock) SendMessage(ctx context.Context, id string, message Message) (AcceptedInput, error) {
	prompt := message.Text
	if err := ctx.Err(); err != nil {
		return AcceptedInput{}, err
	}
	m.mu.Lock()
	s := m.sessions[id]
	if s == nil {
		m.mu.Unlock()
		return AcceptedInput{}, fmt.Errorf("unknown session")
	}
	if s.status != "idle" {
		m.mu.Unlock()
		return AcceptedInput{}, fmt.Errorf("session is not idle")
	}
	run, cancel := context.WithCancel(m.ctx)
	s.cancel = cancel
	s.done = make(chan struct{})
	s.status = "running"
	s.info.UpdatedAt = time.Now().UTC()
	s.prompt = prompt
	s.ready = false
	done := s.done
	step := m.Step
	eventID := m.emit(id, "user.message", map[string]any{"client_request_id": message.CommandID, "content": []any{map[string]string{"type": "text", "text": prompt}}})
	m.emit(id, "session.status_running", nil)
	m.mu.Unlock()
	go func() {
		defer cancel()
		defer close(done)
		steps := []struct {
			kind string
			body map[string]any
		}{
			{"agent.message", map[string]any{"content": []any{map[string]string{"type": "text", "text": "已收到需求，正在规划页面与交互。"}}}},
			{"agent.tool_use", map[string]any{"name": "Write", "input": map[string]string{"file_path": "index.html", "content": "<main><h1>专注当下</h1><button>开始专注</button></main>"}}},
			{"agent.tool_use", map[string]any{"name": "Bash", "input": map[string]string{"command": "npm run build"}}},
			{"agent.tool_result", map[string]any{"content": []any{map[string]string{"type": "text", "text": "✓ Demo build completed. 静态页面已准备好。"}}}},
			{"agent.message", map[string]any{"content": []any{map[string]string{"type": "text", "text": "页面做好了，可以查看效果，也可以继续提出修改。"}}}},
		}
		interrupted := false
		for _, s := range steps {
			select {
			case <-run.Done():
				interrupted = true
			case <-time.After(step):
			}
			if interrupted {
				break
			}
			m.emit(id, s.kind, s.body)
		}
		m.mu.Lock()
		s := m.sessions[id]
		s.status = "idle"
		s.ready = !interrupted
		reason := "end_turn"
		if interrupted {
			reason = "interrupted"
		}
		m.emit(id, "session.status_idle", map[string]any{"stop_reason": map[string]string{"type": reason}})
		m.mu.Unlock()
	}()
	return AcceptedInput{EventID: eventID}, nil
}

func (m *Mock) ListSessions(ctx context.Context, q SessionQuery) (SessionPage, error) {
	if err := ctx.Err(); err != nil {
		return SessionPage{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	all := []Session{}
	for _, item := range m.sessions {
		s := item.info
		s.Status = item.status
		if q.Status != "" && q.Status != s.Status {
			continue
		}
		if !q.IncludeArchived && s.ArchivedAt != nil {
			continue
		}
		if q.CreatedAfter != "" {
			t, _ := time.Parse(time.RFC3339Nano, q.CreatedAfter)
			if s.CreatedAt.Before(t) {
				continue
			}
		}
		if q.CreatedBefore != "" {
			t, _ := time.Parse(time.RFC3339Nano, q.CreatedBefore)
			if s.CreatedAt.After(t) {
				continue
			}
		}
		all = append(all, s)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].ID > all[j].ID
		}
		return all[i].CreatedAt.After(all[j].CreatedAt)
	})
	start := 0
	if q.Page != "" {
		start = -1
		for i, s := range all {
			if s.ID == q.Page {
				start = i + 1
				break
			}
		}
		if start < 0 {
			return SessionPage{}, fmt.Errorf("invalid page")
		}
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 20
	}
	end := min(len(all), start+limit)
	out := SessionPage{Data: all[start:end]}
	if end < len(all) {
		out.NextPage = all[end-1].ID
	}
	return out, nil
}
func (m *Mock) ListEvents(ctx context.Context, id string, q EventQuery) (EventPage, error) {
	if _, err := m.Status(ctx, id); err != nil {
		return EventPage{}, err
	}
	all := []events.Frame{}
	for _, f := range m.hub.Frames(id) {
		if len(q.Types) == 0 {
			all = append(all, f)
		} else {
			for _, kind := range q.Types {
				if kind == f.Type {
					all = append(all, f)
					break
				}
			}
		}
	}
	if q.Order == "desc" {
		for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
			all[i], all[j] = all[j], all[i]
		}
	}
	start := 0
	if q.Page != "" {
		start = -1
		for i, f := range all {
			if f.ID == q.Page {
				start = i + 1
				break
			}
		}
		if start < 0 {
			return EventPage{Data: []events.Frame{}}, nil
		}
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 20
	}
	end := min(len(all), start+limit)
	out := EventPage{Data: all[start:end]}
	if end < len(all) {
		out.NextPage = all[end-1].ID
	}
	return out, nil
}
func (m *Mock) ListFiles(ctx context.Context, id string, limit int, page string) (FilePage, error) {
	files, err := m.Artifacts(ctx, id)
	if err != nil {
		return FilePage{}, err
	}
	out := FilePage{Data: []FileInfo{}}
	if page != "" {
		return out, nil
	}
	for _, f := range files {
		out.Data = append(out.Data, FileInfo{ID: "mock_" + id + "_" + f.Path, Path: f.Path, Size: int64(len(f.Data))})
	}
	return out, nil
}

func (m *Mock) Interrupt(ctx context.Context, id string) error {
	m.mu.Lock()
	s := m.sessions[id]
	if s == nil {
		m.mu.Unlock()
		return fmt.Errorf("unknown session")
	}
	cancel, done := s.cancel, s.done
	running := s.status == "running"
	m.mu.Unlock()
	if !running {
		return nil
	}
	m.emit(id, "user.interrupt", nil)
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (m *Mock) Stream(ctx context.Context, id string, emit func(events.Frame) error) error {
	return m.hub.Replay(ctx, id, "", emit)
}
func (m *Mock) Artifacts(ctx context.Context, id string) ([]File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	s := m.sessions[id]
	if s == nil || !s.ready {
		m.mu.Unlock()
		return nil, nil
	}
	prompt := s.prompt
	m.mu.Unlock()
	color := "#6c5ce7"
	if strings.Contains(prompt, "红") {
		color = "#e64d5c"
	}
	page := `<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>专注当下</title><style>body{margin:0;background:#f8f6f2;color:#222;font:18px system-ui;display:grid;place-items:center;min-height:100vh}main{text-align:center;max-width:650px;padding:30px}small{color:#7a7770}h1{font-size:44px}#clock{font-size:100px;font-variant-numeric:tabular-nums;margin:30px}button{background:` + color + `;border:0;color:white;border-radius:30px;padding:16px 40px;font:inherit;cursor:pointer}p{color:#777;font-size:14px}</style><main><small>LINXI · DEMO ARTIFACT</small><h1>专注当下</h1><div id="clock">25:00</div><button id="start">开始专注</button><p>本轮需求：` + html.EscapeString(prompt) + `</p><p>这是模拟模式的固定示例产物。</p></main><script>let n=1500,t;document.querySelector('#start').onclick=()=>{if(t){clearInterval(t);t=null;document.querySelector('#start').textContent='继续专注';return}document.querySelector('#start').textContent='暂停';t=setInterval(()=>{n=Math.max(0,n-1);document.querySelector('#clock').textContent=String(Math.floor(n/60)).padStart(2,'0')+':'+String(n%60).padStart(2,'0');if(!n){clearInterval(t);t=null}},1000)}</script></html>`
	return []File{{Path: "index.html", Data: []byte(page)}}, nil
}
