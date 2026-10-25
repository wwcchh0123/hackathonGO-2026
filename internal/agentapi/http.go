package agentapi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
	"github.com/wwcchh0123/hackathonGO-2026/internal/events"
)

type HTTP struct {
	base, key, agent, environment string
	version                       int
	vaultIDs                      []string
	client                        anthropic.Client
}

func NewHTTP(base, key, agent, environment string) *HTTP {
	base = strings.TrimRight(base, "/") + "/"
	return &HTTP{
		base: base, key: key, agent: agent, environment: environment,
		client: anthropic.NewClient(
			option.WithoutEnvironmentDefaults(),
			option.WithBaseURL(base),
			option.WithAPIKey(key),
			// Session-level writes are reconciled by Controller; SDK retries would
			// duplicate an ambiguous Create/Send request.
			option.WithMaxRetries(0),
		),
	}
}

// WithVersion pins an existing agent snapshot; zero selects its current version.
func (p *HTTP) WithVersion(version int) *HTTP { p.version = version; return p }
func (p *HTTP) WithVaults(ids []string) *HTTP { p.vaultIDs = append([]string(nil), ids...); return p }

// Validate checks configured resources without creating or mutating them.
// The calls use the version of anthropic-sdk-go recommended by AgentAPI.
func (p *HTTP) Validate(ctx context.Context) error {
	agent, err := p.client.Beta.Agents.Get(ctx, p.agent, anthropic.BetaAgentGetParams{})
	if err != nil {
		return sdkError("GET agent", err)
	}
	if string(agent.Type) != "agent" || agent.JSON.ArchivedAt.Valid() {
		return fmt.Errorf("configured agent must be an available agent")
	}
	var agentMeta struct {
		Runtime string `json:"runtime"`
	}
	if err := json.Unmarshal([]byte(agent.RawJSON()), &agentMeta); err != nil || agentMeta.Runtime != "claudecode" {
		return fmt.Errorf("configured agent must use claudecode runtime")
	}
	// AgentAPI deployments currently expose environment details through the
	// cursor list endpoint, while the generated SDK's item endpoint may return
	// 404 for the same existing environment. Listing also lets us verify that
	// the configured ID belongs to the current API key without mutating it.
	environments, err := p.client.Beta.Environments.List(ctx, anthropic.BetaEnvironmentListParams{
		Limit: param.NewOpt[int64](100),
	})
	if err != nil {
		return sdkError("LIST environments", err)
	}
	var env *anthropic.BetaEnvironment
	for page := environments; page != nil; {
		for i := range page.Data {
			if page.Data[i].ID == p.environment {
				env = &page.Data[i]
				break
			}
		}
		if env != nil || page.NextPage == "" {
			break
		}
		page, err = page.GetNextPage()
		if err != nil {
			return sdkError("LIST environments", err)
		}
	}
	if env == nil || env.Config.Type != "cloud" || env.JSON.ArchivedAt.Valid() {
		return fmt.Errorf("configured environment must be an available cloud environment")
	}
	return nil
}

func (p *HTTP) Create(ctx context.Context, input ...NewSession) (Session, error) {
	params := anthropic.BetaSessionNewParams{
		Agent:         anthropic.BetaSessionNewParamsAgentUnion{OfString: param.NewOpt(p.agent)},
		EnvironmentID: p.environment,
		VaultIDs:      p.vaultIDs,
	}
	if len(input) > 0 {
		params.Title = param.NewOpt(input[0].Title)
		params.Metadata = input[0].Metadata
	}
	if p.version > 0 {
		params.Agent = anthropic.BetaSessionNewParamsAgentUnion{OfBetaManagedAgentsAgents: &anthropic.BetaManagedAgentsAgentParams{
			ID: p.agent, Type: anthropic.BetaManagedAgentsAgentParamsTypeAgent, Version: param.NewOpt(int64(p.version)),
		}}
	}
	res, err := p.client.Beta.Sessions.New(ctx, params)
	if err != nil {
		return Session{}, sdkError("POST session", err)
	}
	if res == nil || res.ID == "" {
		return Session{}, &Error{Operation: "POST session", Ambiguous: true, Cause: fmt.Errorf("empty session id")}
	}
	return decodeSession(res.RawJSON())
}

func (p *HTTP) SendMessage(ctx context.Context, id string, message Message) (AcceptedInput, error) {
	params := anthropic.BetaSessionEventSendParams{Events: []anthropic.BetaManagedAgentsEventParamsUnion{{
		OfUserMessage: &anthropic.BetaManagedAgentsUserMessageEventParams{
			Type: anthropic.BetaManagedAgentsUserMessageEventParamsTypeUserMessage,
			Content: []anthropic.BetaManagedAgentsUserMessageEventParamsContentUnion{{OfText: &anthropic.BetaManagedAgentsTextBlockParam{
				Type: anthropic.BetaManagedAgentsTextBlockTypeText, Text: message.Text,
			}}},
		},
	}}}
	// Keep the SDK's typed request as the source of truth, while adding the
	// correlation payload supported by AgentAPI's event contract.
	res, err := p.client.Beta.Sessions.Events.Send(ctx, id, params, option.WithJSONSet("events", []map[string]any{{
		"type":    "user.message",
		"input":   map[string]any{"parts": []map[string]string{{"type": "text", "text": message.Text}}},
		"payload": map[string]string{"client_request_id": message.CommandID, "source": "linxi-control"},
	}}))
	if err != nil {
		return AcceptedInput{}, sdkError("POST message", err)
	}
	if res != nil {
		for _, event := range res.Data {
			if event.Type == "user.message" && event.ID != "" {
				return AcceptedInput{EventID: event.ID}, nil
			}
		}
	}
	// Some deployments return an empty acknowledgement; the stream reconciliation
	// path can still match the accepted event by its correlation payload.
	return AcceptedInput{}, nil
}
func (p *HTTP) Send(ctx context.Context, id, prompt string) error {
	_, err := p.SendMessage(ctx, id, Message{Text: prompt})
	return err
}

func (p *HTTP) Interrupt(ctx context.Context, id string) error {
	_, err := p.client.Beta.Sessions.Events.Send(ctx, id, anthropic.BetaSessionEventSendParams{Events: []anthropic.BetaManagedAgentsEventParamsUnion{{
		OfUserInterrupt: &anthropic.BetaManagedAgentsUserInterruptEventParams{Type: anthropic.BetaManagedAgentsUserInterruptEventParamsTypeUserInterrupt},
	}}})
	if err != nil {
		return sdkError("POST interrupt", err)
	}
	return nil
}
func (p *HTTP) Status(ctx context.Context, id string) (Session, error) {
	res, err := p.client.Beta.Sessions.Get(ctx, id, anthropic.BetaSessionGetParams{})
	if err != nil {
		return Session{}, sdkError("GET session", err)
	}
	if res == nil || res.ID != id || !KnownStatus(string(res.Status)) {
		return Session{}, fmt.Errorf("AgentAPI returned invalid session state")
	}
	return decodeSession(res.RawJSON())
}

// decodeSession deliberately projects an allowlist: resources, credentials and
// the Agent's internal system/tool configuration never reach device responses.
func decodeSession(raw string) (Session, error) {
	var wire struct {
		Session
		Metadata map[string]string `json:"metadata"`
		Agent    struct {
			Runtime string `json:"runtime"`
		} `json:"agent"`
	}
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		return Session{}, fmt.Errorf("invalid session response")
	}
	wire.Session.Runtime = wire.Agent.Runtime
	wire.Session.Metadata = wire.Metadata
	return wire.Session, nil
}

func (p *HTTP) ListSessions(ctx context.Context, q SessionQuery) (SessionPage, error) {
	params := anthropic.BetaSessionListParams{Limit: param.NewOpt(int64(q.Limit)), IncludeArchived: param.NewOpt(q.IncludeArchived)}
	if q.Page != "" {
		params.Page = param.NewOpt(q.Page)
	}
	// Session catalog queries are scoped to the Agent configured for this
	// provider. The API key may see sessions belonging to several Agents; the
	// control service must never leak those into Linxi's task switcher. Keep the
	// query field as a compatibility fallback for an unconfigured test client,
	// but ignore caller-provided AgentID whenever this provider has a configured
	// Agent.
	agentID := p.agent
	if agentID == "" {
		agentID = q.AgentID
	}
	if agentID != "" {
		params.AgentID = param.NewOpt(agentID)
	}
	var opts []option.RequestOption
	// AgentAPI's public contract uses a singular status and fixed created_at desc.
	// Do not send the SDK's CMA statuses[] or order fields.
	if q.Status != "" {
		opts = append(opts, option.WithQuery("status", q.Status))
	}
	if q.CreatedAfter != "" {
		opts = append(opts, option.WithQuery("created_at[gte]", q.CreatedAfter))
	}
	if q.CreatedBefore != "" {
		opts = append(opts, option.WithQuery("created_at[lte]", q.CreatedBefore))
	}
	res, err := p.client.Beta.Sessions.List(ctx, params, opts...)
	if err != nil {
		return SessionPage{}, sdkError("GET sessions", err)
	}
	out := SessionPage{Data: []Session{}, NextPage: res.NextPage}
	for _, item := range res.Data {
		s, err := decodeSession(item.RawJSON())
		if err != nil {
			return SessionPage{}, err
		}
		out.Data = append(out.Data, s)
	}
	return out, nil
}
func (p *HTTP) ListEvents(ctx context.Context, id string, q EventQuery) (EventPage, error) {
	params := anthropic.BetaSessionEventListParams{Limit: param.NewOpt(int64(q.Limit)), Order: anthropic.BetaSessionEventListParamsOrder(q.Order), Types: q.Types}
	if q.Page != "" {
		params.Page = param.NewOpt(q.Page)
	}
	res, err := p.client.Beta.Sessions.Events.List(ctx, id, params)
	if err != nil {
		return EventPage{}, sdkError("GET events", err)
	}
	out := EventPage{Data: []events.Frame{}, NextPage: res.NextPage}
	for _, e := range res.Data {
		out.Data = append(out.Data, events.Frame{ID: e.ID, Type: e.Type, Data: json.RawMessage(e.RawJSON())})
	}
	return out, nil
}
func (p *HTTP) ListFiles(ctx context.Context, id string, limit int, page string) (FilePage, error) {
	params := anthropic.BetaFileListParams{ScopeID: param.NewOpt(id), Limit: param.NewOpt(int64(limit))}
	if page != "" {
		params.Page = param.NewOpt(page)
	}
	res, err := p.client.Beta.Files.List(ctx, params)
	if err != nil {
		return FilePage{}, sdkError("GET files", err)
	}
	out := FilePage{Data: []FileInfo{}, NextPage: res.NextPage}
	for _, f := range res.Data {
		out.Data = append(out.Data, FileInfo{ID: f.ID, Path: f.Filename, Size: f.SizeBytes, CreatedAt: f.CreatedAt})
	}
	return out, nil
}
func (p *HTTP) Stream(ctx context.Context, id string, emit func(events.Frame) error) error {
	stream := p.client.Beta.Sessions.Events.StreamEvents(ctx, id, anthropic.BetaSessionEventStreamParams{})
	defer stream.Close()
	for stream.Next() {
		event := stream.Current()
		if err := emit(events.Frame{ID: event.ID, Type: event.Type, Data: json.RawMessage(event.RawJSON())}); err != nil {
			return err
		}
	}
	if err := stream.Err(); err != nil {
		return sdkError("GET event stream", err)
	}
	return io.EOF
}

func (p *HTTP) Artifacts(ctx context.Context, id string) ([]File, error) {
	pager := p.client.Beta.Files.ListAutoPaging(ctx, anthropic.BetaFileListParams{ScopeID: param.NewOpt(id), Limit: param.NewOpt(int64(100))})
	type reference struct {
		ID, Filename string
		CreatedAt    time.Time
	}
	latest := make(map[string]reference)
	count := 0
	for pager.Next() {
		count++
		if count > 500 {
			return nil, fmt.Errorf("preview file history exceeds 500 exports; narrow the outputs")
		}
		f := pager.Current()
		previous, exists := latest[f.Filename]
		if !exists || f.CreatedAt.After(previous.CreatedAt) {
			latest[f.Filename] = reference{ID: f.ID, Filename: f.Filename, CreatedAt: f.CreatedAt}
		}
		if len(latest) > 100 {
			return nil, fmt.Errorf("preview supports at most 100 files")
		}
	}
	if err := pager.Err(); err != nil {
		return nil, sdkError("GET files", err)
	}
	names := make([]string, 0, len(latest))
	for name := range latest {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]File, 0, len(names))
	total := 0
	for _, name := range names {
		f := latest[name]
		resp, err := p.client.Beta.Files.Download(ctx, f.ID, anthropic.BetaFileDownloadParams{})
		if err != nil {
			return nil, sdkError("GET file content", err)
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, (10<<20)+1))
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if len(b) > 10<<20 {
			return nil, fmt.Errorf("artifact exceeds 10 MiB")
		}
		total += len(b)
		if total > 64<<20 {
			return nil, fmt.Errorf("preview exceeds 64 MiB")
		}
		out = append(out, File{Path: f.Filename, Data: b})
	}
	return out, nil
}

var statusCodePattern = regexp.MustCompile(`\b([45][0-9]{2})\b`)

func sdkError(operation string, err error) error {
	if err == nil {
		return nil
	}
	wrapped := &Error{Operation: operation, Cause: err, Ambiguous: strings.HasPrefix(operation, "POST")}
	if match := statusCodePattern.FindStringSubmatch(err.Error()); len(match) == 2 {
		fmt.Sscanf(match[1], "%d", &wrapped.StatusCode)
	}
	if wrapped.StatusCode >= 400 && wrapped.StatusCode < 500 && wrapped.StatusCode != 408 {
		wrapped.Ambiguous = false
	}
	return wrapped
}

// DecodeSSE remains a small compatibility helper for local contract tests and
// non-SDK fixtures; the live HTTP Provider consumes streams with the SDK above.
func DecodeSSE(r io.Reader, emit func(events.Frame) error) error {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 4096), 4<<20)
	var f events.Frame
	var data []string
	flush := func() error {
		if len(data) == 0 {
			f = events.Frame{}
			return nil
		}
		f.Data = json.RawMessage(strings.Join(data, "\n"))
		var v struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		}
		if err := json.Unmarshal(f.Data, &v); err != nil {
			return fmt.Errorf("invalid AgentAPI event: %w", err)
		}
		if v.ID != "" {
			f.ID = v.ID
		}
		if v.Type != "" {
			f.Type = v.Type
		}
		err := emit(f)
		f = events.Frame{}
		data = nil
		return err
	}
	for s.Scan() {
		line := s.Text()
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		name, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch name {
		case "id":
			f.ID = value
		case "event":
			f.Type = value
		case "data":
			data = append(data, value)
		}
	}
	if err := s.Err(); err != nil {
		return err
	}
	if err := flush(); err != nil {
		return err
	}
	return io.EOF
}
