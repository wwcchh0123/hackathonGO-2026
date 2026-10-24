package agentapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/wwcchh0123/hackathonGO-2026/internal/events"
)

type HTTP struct {
	base, key, agent, environment string
	client                        *http.Client
}

func NewHTTP(base, key, agent, environment string) *HTTP {
	// A stream has no overall timeout; request contexts own cancellation.
	return &HTTP{strings.TrimRight(base, "/"), key, agent, environment, &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: 30 * time.Second}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (p *HTTP) request(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var data io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		data = bytes.NewReader(b)
	}
	r, err := http.NewRequestWithContext(ctx, method, p.base+path, data)
	if err != nil {
		return nil, err
	}
	r.Header.Set("x-api-key", p.key)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := p.client.Do(r)
	if err != nil {
		return nil, fmt.Errorf("AgentAPI request failed: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, fmt.Errorf("AgentAPI %s: HTTP %d", method, resp.StatusCode)
	}
	return resp, nil
}
func (p *HTTP) json(ctx context.Context, method, path string, body, out any) error {
	r, err := p.request(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if out == nil {
		_, err = io.Copy(io.Discard, io.LimitReader(r.Body, 1<<20))
		return err
	}
	return json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(out)
}
func (p *HTTP) Create(ctx context.Context) (Session, error) {
	var s Session
	err := p.json(ctx, "POST", "/v1/sessions", map[string]any{"agent": p.agent, "environment_id": p.environment}, &s)
	if err == nil && s.ID == "" {
		err = fmt.Errorf("AgentAPI returned an empty session id")
	}
	return s, err
}
func sessionPath(id string) string { return "/v1/sessions/" + url.PathEscape(id) }
func (p *HTTP) Send(ctx context.Context, id, prompt string) error {
	return p.json(ctx, "POST", sessionPath(id)+"/events", map[string]any{"events": []any{map[string]any{"type": "user.message", "input": map[string]any{"parts": []any{map[string]string{"type": "text", "text": prompt}}}}}}, nil)
}
func (p *HTTP) Interrupt(ctx context.Context, id string) error {
	return p.json(ctx, "POST", sessionPath(id)+"/events", map[string]any{"events": []any{map[string]string{"type": "user.interrupt"}}}, nil)
}
func (p *HTTP) Status(ctx context.Context, id string) (Session, error) {
	var s Session
	err := p.json(ctx, "GET", sessionPath(id), nil, &s)
	return s, err
}
func (p *HTTP) Stream(ctx context.Context, id string, emit func(events.Frame) error) error {
	r, err := p.request(ctx, "GET", sessionPath(id)+"/events/stream", nil)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	return DecodeSSE(r.Body, emit)
}

// DecodeSSE preserves provider JSON, including fields unknown to this version.
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
func (p *HTTP) Artifacts(ctx context.Context, id string) ([]File, error) {
	type reference struct {
		ID        string    `json:"id"`
		Filename  string    `json:"filename"`
		CreatedAt time.Time `json:"created_at"`
	}
	latest := make(map[string]reference)
	pages := make(map[string]bool)
	page := ""
	for {
		if pages[page] {
			return nil, fmt.Errorf("repeated file page cursor")
		}
		pages[page] = true
		q := url.Values{"scope_id": {id}, "limit": {"100"}}
		if page != "" {
			q.Set("page", page)
		}
		var listing struct {
			Data     []reference `json:"data"`
			NextPage string      `json:"next_page"`
		}
		if err := p.json(ctx, "GET", "/v1/files?"+q.Encode(), nil, &listing); err != nil {
			return nil, err
		}
		for _, f := range listing.Data {
			previous, exists := latest[f.Filename]
			// A session can export index.html again on later turns. Never let
			// an older version overwrite the latest when replaying all files.
			if !exists || f.CreatedAt.After(previous.CreatedAt) {
				latest[f.Filename] = f
			}
		}
		if len(latest) > 100 {
			return nil, fmt.Errorf("preview supports at most 100 files")
		}
		if listing.NextPage == "" {
			break
		}
		page = listing.NextPage
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
		r, err := p.request(ctx, "GET", "/v1/files/"+url.PathEscape(f.ID)+"/content", nil)
		if err != nil {
			return nil, err
		}
		b, err := io.ReadAll(io.LimitReader(r.Body, (10<<20)+1))
		r.Body.Close()
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
