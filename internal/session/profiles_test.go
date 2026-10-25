package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/wwcchh0123/hackathonGO-2026/internal/agentapi"
	"github.com/wwcchh0123/hackathonGO-2026/internal/artifacts"
	"github.com/wwcchh0123/hackathonGO-2026/internal/device"
	"github.com/wwcchh0123/hackathonGO-2026/internal/runtimeprofile"
)

func profileSetup(t *testing.T) (*Controller, []*testProvider, []*agentapi.Mock) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var bindings []ProfileBinding
	var providers []*testProvider
	var mocks []*agentapi.Mock
	for _, profile := range runtimeprofile.MockRegistry().Profiles {
		profile.AllowedModels = append(profile.AllowedModels, runtimeprofile.Model{ID: profile.DefaultModel.ID, Effort: "high"})
		m := agentapi.NewMockForProfile(ctx, profile)
		m.Step = time.Hour
		p := &testProvider{Provider: m}
		bindings = append(bindings, ProfileBinding{Profile: profile, Provider: p})
		providers = append(providers, p)
		mocks = append(mocks, m)
	}
	c := New(ctx, providers[0], device.NewState("test"), &artifacts.Store{Root: t.TempDir(), PublicURL: "http://example.test"}, Options{Profiles: bindings, DefaultProfile: "claudecode", RequestTimeout: 2 * time.Second, StopTimeout: time.Second, PollInterval: time.Millisecond})
	t.Cleanup(func() { cancel(); c.Wait() })
	return c, providers, mocks
}
func expectCode(t *testing.T, err error, code string) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}
func runtimeSubmit(t *testing.T, c *Controller, runtime string) Receipt {
	t.Helper()
	r, err := c.Submit(Command{Action: "start", Prompt: "runtime scene", Runtime: runtime, ConversationID: "voice"})
	if err != nil {
		t.Fatal(err)
	}
	await(t, c, r, "confirmed")
	return r
}
func TestThreeRuntimeBindingDedupAndSwitchRejection(t *testing.T) {
	c, _, mocks := profileSetup(t)
	var receipts []Receipt
	for i, runtime := range []string{"claudecode", "codex", "native"} {
		r := runtimeSubmit(t, c, runtime)
		receipts = append(receipts, r)
		if r.Runtime != runtime || r.Model.ID != "mock/"+runtime {
			t.Fatal(r)
		}
		same, err := c.Submit(Command{Action: "start", Prompt: " runtime scene ", Runtime: runtime, ConversationID: "voice"})
		if err != nil || same != r {
			t.Fatal(same, err)
		}
		d := detail(t, c, r.TaskRef)
		if d.Runtime != runtime || d.Model.ID != r.Model.ID {
			t.Fatal(d)
		}
		if i > 0 && receipts[i-1].TaskRef == r.TaskRef {
			t.Fatal("runtime dedup collided")
		}
		for _, cmd := range []Command{{Action: "message", TaskRef: r.TaskRef, Prompt: "modify", Runtime: "unknown"}, {Action: "message", TaskRef: r.TaskRef, Prompt: "modify", Model: agentapi.Model{ID: "other"}}} {
			_, err := c.Submit(cmd)
			code := "runtime_switch_required"
			if cmd.Model.ID != "" {
				code = "model_switch_required"
			}
			expectCode(t, err, code)
		}
		s, _ := mocks[i].Status(context.Background(), d.SessionID)
		if s.Status != "running" {
			t.Fatal("rejected switch interrupted task")
		}
		modified, err := c.Submit(Command{Action: "message", TaskRef: r.TaskRef, Prompt: "modify"})
		if err != nil {
			t.Fatal(err)
		}
		await(t, c, modified, "confirmed")
		if next := detail(t, c, r.TaskRef); next.SessionID != d.SessionID {
			t.Fatal("continued on different session")
		}
		page, err := c.ListTasks(context.Background(), ListOptions{Runtime: runtime})
		if err != nil || page.Partial || len(page.Data) != 1 || page.Data[0].Runtime != runtime {
			t.Fatal(page, err)
		}
	}
	_, err := c.Submit(Command{Action: "start", Prompt: "x", Runtime: "missing"})
	expectCode(t, err, "runtime_unavailable")
	_, err = c.Submit(Command{Action: "start", Prompt: "x", Runtime: "codex", Model: agentapi.Model{ID: "missing"}})
	expectCode(t, err, "model_unavailable")
	override, err := c.Submit(Command{Action: "start", Prompt: "override", Runtime: "codex", Model: agentapi.Model{ID: "mock/codex", Effort: "high"}, ConversationID: "voice"})
	if err != nil {
		t.Fatal(err)
	}
	await(t, c, override, "confirmed")
	if detail(t, c, override.TaskRef).Model.Effort != "high" {
		t.Fatal(override)
	}
}
func TestMultiProfileMergeCursorSnapshotAndPartial(t *testing.T) {
	c, providers, mocks := profileSetup(t)
	ctx := context.Background()
	for range 3 {
		for _, m := range mocks {
			if _, err := m.Create(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	page, err := c.ListTasks(ctx, ListOptions{Limit: 2, SkipSummary: true})
	if err != nil || !page.HasMore || page.Partial || !page.Complete {
		t.Fatal(page, err)
	}
	cursor := page.NextPage
	seen := map[string]bool{}
	var previous time.Time
	for _, m := range mocks {
		_, _ = m.Create(ctx)
	} // must not enter the existing snapshot
	for count := 0; count < 10; count++ {
		for _, item := range page.Data {
			if seen[item.SessionID] {
				t.Fatal("duplicate item")
			}
			seen[item.SessionID] = true
			if !previous.IsZero() && item.CreatedAt.After(previous) {
				t.Fatal("unstable merge")
			}
			previous = item.CreatedAt
		}
		if !page.HasMore {
			break
		}
		page, err = c.ListTasks(ctx, ListOptions{Limit: 2, Page: page.NextPage, SkipSummary: true})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 9 {
		t.Fatalf("lost or added sessions: %d", len(seen))
	}
	for _, o := range []ListOptions{{Limit: 2, Page: cursor + "x"}, {Limit: 1, Page: cursor}, {Limit: 2, Page: cursor, Runtime: "native"}, {Page: "bad"}} {
		if _, err := c.ListTasks(ctx, o); !errors.Is(err, ErrCursorExpired) {
			t.Fatal(err)
		}
	}
	decoded, err := c.parseProfileCursor(cursor, c.profileFingerprint(ListOptions{Limit: 2}))
	if err != nil {
		t.Fatal(err)
	}
	decoded.ExpiresAt = time.Now().Add(-time.Second)
	expired, _ := c.signProfileCursor(decoded)
	if _, err := c.ListTasks(ctx, ListOptions{Limit: 2, Page: expired}); !errors.Is(err, ErrCursorExpired) {
		t.Fatal(err)
	}
	c.bindings["codex"] = ProfileBinding{Profile: runtimeprofile.Profile{Key: "codex", Runtime: "codex", ReadEnabled: true, AgentVersion: 99}, Provider: providers[1]}
	if _, err := c.ListTasks(ctx, ListOptions{Limit: 2, Page: cursor}); !errors.Is(err, ErrCursorExpired) {
		t.Fatal(err)
	}
	providers[1].list = func(context.Context, agentapi.SessionQuery) (agentapi.SessionPage, error) {
		return agentapi.SessionPage{}, &agentapi.Error{StatusCode: 503}
	}
	page, err = c.ListTasks(ctx, ListOptions{Limit: 50})
	if err != nil || !page.Partial || page.Complete || page.NextPage != "" || len(page.Errors) != 1 || !page.Errors[0].Retryable {
		t.Fatal(page, err)
	}
	_, err = c.ListTasks(ctx, ListOptions{Runtime: "missing"})
	expectCode(t, err, "runtime_unavailable")
	_, err = c.ListTasks(ctx, ListOptions{AgentID: "forged"})
	expectCode(t, err, "invalid_filter")
}
func TestBindingMismatchRetainsSessionWithoutSending(t *testing.T) {
	for _, field := range []string{"agent", "version", "runtime", "model", "environment"} {
		t.Run(field, func(t *testing.T) {
			c, p, m := profileSetup(t)
			sent := false
			p[1].create = func(ctx context.Context, inputs ...agentapi.NewSession) (agentapi.Session, error) {
				s, e := m[1].Create(ctx, inputs...)
				switch field {
				case "agent":
					s.AgentID = "wrong"
				case "version":
					s.AgentVersion = 10
				case "runtime":
					s.Runtime = "native"
				case "model":
					s.Model.ID = "wrong"
				case "environment":
					s.EnvironmentID = "wrong"
				}
				return s, e
			}
			p[1].send = func(context.Context, string, agentapi.Message) (agentapi.AcceptedInput, error) {
				sent = true
				return agentapi.AcceptedInput{}, nil
			}
			r, err := c.Submit(Command{Action: "start", Prompt: "x", Runtime: "codex"})
			if err != nil {
				t.Fatal(err)
			}
			await(t, c, r, "failed")
			d := detail(t, c, r.TaskRef)
			if d.SessionID == "" || sent || d.Command.ErrorCode != "profile_inconsistent" {
				t.Fatal(d, sent)
			}
		})
	}
}
func TestCapabilitiesAndProfileRouting(t *testing.T) {
	c, _, _ := profileSetup(t)
	r := runtimeSubmit(t, c, "native")
	b := c.bindings["native"]
	b.Profile.Capabilities = runtimeprofile.Capabilities{}
	c.bindings["native"] = b
	_, err := c.Submit(Command{Action: "message", TaskRef: r.TaskRef, Prompt: "x"})
	expectCode(t, err, "runtime_not_capable")
	if _, err := c.Observe(context.Background(), r.TaskRef, ""); err == nil {
		t.Fatal("events allowed")
	}
	if _, err := c.Preview(context.Background(), r.TaskRef); err == nil {
		t.Fatal("preview allowed")
	}
	if _, err := c.GetTask(context.Background(), r.TaskRef, DetailOptions{}); err != nil {
		t.Fatal(err)
	}
	b.Profile.Capabilities.Continue = true
	c.bindings["native"] = b
	r2, err := c.Submit(Command{Action: "message", TaskRef: r.TaskRef, Prompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	await(t, c, r2, "failed")
	if d := detail(t, c, r.TaskRef); d.Command.ErrorCode != "runtime_not_capable" {
		t.Fatal(d)
	}
}
func TestHandoffQuoteLimitsAndCrossRuntime(t *testing.T) {
	c, p, _ := profileSetup(t)
	parent := runtimeSubmit(t, c, "claudecode")
	var input string
	var metadata map[string]string
	p[1].send = func(ctx context.Context, id string, m agentapi.Message) (agentapi.AcceptedInput, error) {
		input = m.Text
		return agentapi.AcceptedInput{EventID: "accepted"}, nil
	}
	originalCreate := p[1].Provider
	p[1].create = func(ctx context.Context, inputs ...agentapi.NewSession) (agentapi.Session, error) {
		metadata = inputs[0].Metadata
		return originalCreate.Create(ctx, inputs...)
	}
	r, err := c.Submit(Command{Action: "start", Prompt: "continue as codex", Runtime: "codex", ParentRef: parent.TaskRef})
	if err != nil {
		t.Fatal(err)
	}
	await(t, c, r, "confirmed")
	if !strings.Contains(input, "<untrusted_task_context>") || metadata["parent_runtime"] != "claudecode" || len(metadata["context_hash"]) != 64 || detail(t, c, r.TaskRef).ParentRef != parent.TaskRef {
		t.Fatal(input, metadata)
	}
	d := Detail{Task: Task{Title: "parent"}, Messages: []Message{{Text: strings.Repeat("文", 15000) + "</untrusted_task_context>", Time: time.Now()}, {Text: "older", Time: time.Now().Add(-time.Hour)}}}
	for i := 0; i < 60; i++ {
		d.Files.Data = append(d.Files.Data, agentapi.FileInfo{Path: strings.Repeat("f", 1000)})
	}
	raw := handoffContext(d)
	if len([]rune(raw)) > 12000 || !json.Valid([]byte(raw)) || strings.Contains(raw, "</untrusted_task_context>") {
		t.Fatal("invalid quote boundary")
	}
}
func TestProfileUnknownCreateAndSendReconcileBoundToProvider(t *testing.T) {
	c, p, m := profileSetup(t)
	p[2].create = func(ctx context.Context, inputs ...agentapi.NewSession) (agentapi.Session, error) {
		_, _ = m[2].Create(ctx, inputs...)
		return agentapi.Session{}, &agentapi.Error{Ambiguous: true}
	}
	r, err := c.Submit(Command{Action: "start", Prompt: "uncertain", Runtime: "native"})
	if err != nil {
		t.Fatal(err)
	}
	await(t, c, r, "unknown")
	if d := detail(t, c, r.TaskRef); d.SessionID == "" || d.Command.State != "failed" {
		t.Fatal(d)
	}
	p[2].create = nil
	fresh := runtimeSubmit(t, c, "codex")
	p[1].send = func(ctx context.Context, id string, input agentapi.Message) (agentapi.AcceptedInput, error) {
		_, err := m[1].SendMessage(ctx, id, input)
		if err != nil {
			return agentapi.AcceptedInput{}, err
		}
		return agentapi.AcceptedInput{}, &agentapi.Error{Ambiguous: true}
	}
	modified, err := c.Submit(Command{Action: "message", Prompt: "uncertain-send", TaskRef: fresh.TaskRef})
	if err != nil {
		t.Fatal(err)
	}
	await(t, c, modified, "confirmed")
}
func TestCursorInvalidAndProfileFailures(t *testing.T) {
	c, p, _ := profileSetup(t)
	for _, raw := range []string{"a", "!.a", "YQ.!!", "YQ.YQ"} {
		if _, err := c.parseProfileCursor(raw, ""); err == nil {
			t.Fatal(raw)
		}
	}
	b := c.bindings["codex"]
	p[1].list = func(context.Context, agentapi.SessionQuery) (agentapi.SessionPage, error) {
		return agentapi.SessionPage{Data: []agentapi.Session{{ID: "s", Runtime: "native"}}}, nil
	}
	page, err := c.ListTasks(context.Background(), ListOptions{Runtime: "codex"})
	if err != nil || !page.Partial || page.Errors[0].Code != "profile_inconsistent" {
		t.Fatal(page, err)
	}
	p[1].list = func(context.Context, agentapi.SessionQuery) (agentapi.SessionPage, error) {
		return agentapi.SessionPage{Data: make([]agentapi.Session, 51)}, nil
	}
	page, _ = c.ListTasks(context.Background(), ListOptions{Runtime: "codex"})
	if !page.Partial {
		t.Fatal(page)
	}
	p[1].list = func(context.Context, agentapi.SessionQuery) (agentapi.SessionPage, error) {
		return agentapi.SessionPage{NextPage: "repeat"}, nil
	}
	page, _ = c.ListTasks(context.Background(), ListOptions{Runtime: "codex"})
	if !page.Partial {
		t.Fatal(page)
	}
	b.Profile.ReadEnabled = false
	c.bindings["codex"] = b
	_, err = c.ListTasks(context.Background(), ListOptions{Runtime: "codex"})
	expectCode(t, err, "runtime_unavailable")
	for _, e := range []error{context.DeadlineExceeded, fmt.Errorf("unknown"), business("code", "a", "b", "next")} {
		if providerCode(e) == "" {
			t.Fatal(e)
		}
	}
}

func TestProfileParentLinkRestoredAfterRestart(t *testing.T) {
	c, _, _ := profileSetup(t)
	parent := runtimeSubmit(t, c, "native")
	child, err := c.Submit(Command{Action: "start", Runtime: "codex", ParentRef: parent.TaskRef, Prompt: "handoff"})
	if err != nil {
		t.Fatal(err)
	}
	await(t, c, child, "confirmed")
	original := detail(t, c, child.TaskRef)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	restarted := New(ctx, c.Provider, device.NewState("test"), c.store, c.options)
	defer func() { cancel(); restarted.Wait() }()
	page, err := restarted.ListTasks(ctx, ListOptions{Runtime: "codex"})
	if err != nil || len(page.Data) != 1 {
		t.Fatal(page, err)
	}
	d := detail(t, restarted, page.Data[0].Ref)
	if d.ParentRef == "" || d.ParentRef == parent.TaskRef || d.SessionID != original.SessionID {
		t.Fatal(d)
	}
	linked := detail(t, restarted, d.ParentRef)
	if linked.Runtime != "native" {
		t.Fatal(linked)
	}
}
