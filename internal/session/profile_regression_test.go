package session

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wwcchh0123/hackathonGO-2026/internal/agentapi"
	"github.com/wwcchh0123/hackathonGO-2026/internal/artifacts"
	"github.com/wwcchh0123/hackathonGO-2026/internal/device"
	"github.com/wwcchh0123/hackathonGO-2026/internal/runtimeprofile"
)

func TestRuntimeSelectionDoesNotAcceptProfileKeys(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var bindings []ProfileBinding
	for _, profile := range runtimeprofile.MockRegistry().Profiles {
		profile.Key += "-v2"
		m := agentapi.NewMockForProfile(ctx, profile)
		bindings = append(bindings, ProfileBinding{Profile: profile, Provider: m})
	}
	c := New(ctx, bindings[0].Provider, device.NewState("test"), &artifacts.Store{Root: t.TempDir()}, Options{Profiles: bindings, DefaultProfile: "codex-v2"})
	defer func() { cancel(); c.Wait() }()
	for _, runtime := range []string{"native", "codex", "claudecode", ""} {
		receipt, err := c.Submit(Command{Action: "start", Prompt: "runtime selection", Runtime: runtime})
		if err != nil {
			t.Fatal(err)
		}
		expected := runtime
		if expected == "" {
			expected = "codex"
		}
		if receipt.Runtime != expected {
			t.Fatalf("requested %q, selected %q", runtime, receipt.Runtime)
		}
		await(t, c, receipt, "confirmed")
	}
	_, err := c.Submit(Command{Action: "start", Prompt: "key is not a runtime", Runtime: "codex-v2"})
	expectCode(t, err, "runtime_unavailable")
	_, err = c.ListTasks(ctx, ListOptions{Runtime: "native-v2"})
	expectCode(t, err, "runtime_unavailable")
}

// This is the review reproducer: 150 legitimate task records with 100-character
// titles used to produce a 46KB token that the next request rejected.
func TestLargeLookaheadCanBePagedWithShortCursors(t *testing.T) {
	c, providers, _ := profileSetup(t)
	random := rand.New(rand.NewPCG(42, 54))
	now := time.Now().Add(-time.Hour)
	for i, p := range providers {
		binding := c.options.Profiles[i]
		rows := make([]agentapi.Session, 50)
		for j := range rows {
			title := make([]rune, 100)
			for k := range title {
				title[k] = rune(0x4e00 + random.IntN(0x4fff))
			}
			rows[j] = agentapi.Session{ID: fmt.Sprintf("ses_%016x%016x", random.Uint64(), random.Uint64()), Title: string(title), Runtime: binding.Profile.Runtime, AgentID: binding.Profile.AgentID, AgentVersion: binding.Profile.AgentVersion, EnvironmentID: binding.Profile.EnvironmentID, Model: modelFromProfile(binding.Profile.DefaultModel), Status: "idle", CreatedAt: now.Add(-time.Duration(j) * time.Second)}
		}
		p.list = func(context.Context, agentapi.SessionQuery) (agentapi.SessionPage, error) {
			return agentapi.SessionPage{Data: rows}, nil
		}
	}
	seen := map[string]bool{}
	pageToken := ""
	for pages := 0; pages < 10; pages++ {
		page, err := c.ListTasks(context.Background(), ListOptions{Limit: 20, Page: pageToken, SkipSummary: true})
		if err != nil {
			t.Fatal(err)
		}
		for _, task := range page.Data {
			if seen[task.SessionID] {
				t.Fatal("duplicate session", task.SessionID)
			}
			seen[task.SessionID] = true
		}
		if !page.HasMore {
			break
		}
		if len(page.NextPage) > 128 {
			t.Fatalf("cursor is too large for the wire: %d", len(page.NextPage))
		}
		pageToken = page.NextPage
	}
	if len(seen) != 150 {
		t.Fatalf("lost tasks across pages: got %d", len(seen))
	}
}

func TestSlowProfileDoesNotHideHealthyRuntimes(t *testing.T) {
	c, providers, mocks := profileSetup(t)
	ctx := context.Background()
	for _, m := range mocks {
		if _, err := m.Create(ctx); err != nil {
			t.Fatal(err)
		}
	}
	c.options.RequestTimeout = 300 * time.Millisecond
	providers[0].list = func(ctx context.Context, _ agentapi.SessionQuery) (agentapi.SessionPage, error) {
		<-ctx.Done()
		return agentapi.SessionPage{}, ctx.Err()
	}
	page, err := c.ListTasks(ctx, ListOptions{Limit: 20, SkipSummary: true})
	if err != nil || len(page.Data) != 2 || !page.Partial || page.Complete || page.NextPage != "" {
		t.Fatalf("healthy results lost: %+v %v", page, err)
	}
	if len(page.Errors) != 1 || page.Errors[0].Runtime != "claudecode" || page.Errors[0].Code != "provider_timeout" {
		t.Fatal(page.Errors)
	}
	for _, task := range page.Data {
		if task.Runtime == "claudecode" {
			t.Fatal("failed profile returned a task")
		}
	}
}

func TestFullPageDoesNotFetchAnotherUpstreamPage(t *testing.T) {
	c, providers, mocks := profileSetup(t)
	ctx := context.Background()
	for range 50 {
		if _, err := mocks[0].Create(ctx); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	providers[0].list = func(ctx context.Context, q agentapi.SessionQuery) (agentapi.SessionPage, error) {
		calls++
		if q.Page != "" {
			return agentapi.SessionPage{}, errors.New("next page is unavailable")
		}
		page, err := mocks[0].ListSessions(ctx, q)
		page.NextPage = "more"
		return page, err
	}
	page, err := c.ListTasks(ctx, ListOptions{Runtime: "claudecode", Limit: 50, SkipSummary: true})
	if err != nil || len(page.Data) != 50 || page.Partial || !page.HasMore || calls != 1 {
		t.Fatalf("unnecessary upstream read: calls=%d page=%+v err=%v", calls, page, err)
	}
}

func cursorFixture(c *Controller, fingerprint string) profileCursor {
	return profileCursor{Fingerprint: fingerprint, Generation: c.profileGeneration(), Snapshot: time.Now().UTC(), ExpiresAt: time.Now().Add(profileCursorTTL), Profiles: map[string]profileCursorEntry{"claudecode": {Done: true, Buffer: []agentapi.Session{{ID: "s", Title: "original"}}}}}
}

func TestCursorCacheAuthenticityExpiryAndSnapshotIsolation(t *testing.T) {
	c, _, _ := profileSetup(t)
	cursor := cursorFixture(c, "q")
	token, err := c.signProfileCursor(cursor)
	if err != nil {
		t.Fatal(err)
	}
	cursor.Profiles["claudecode"].Buffer[0].Title = "changed after signing"
	loaded, err := c.parseProfileCursor(token, "q")
	if err != nil || loaded.Profiles["claudecode"].Buffer[0].Title != "original" {
		t.Fatal(loaded, err)
	}
	parts := strings.Split(token, ".")
	for _, raw := range []string{token + "x", "v3." + parts[1] + "." + parts[2], "v2." + parts[1] + ".!!", "v2." + strings.Repeat("a", 32) + "." + parts[2]} {
		if _, err := c.parseProfileCursor(raw, "q"); !errors.Is(err, ErrCursorExpired) {
			t.Fatal("accepted invalid token", err)
		}
	}
	if _, err := c.parseProfileCursor(token, "another-query"); !errors.Is(err, ErrCursorExpired) {
		t.Fatal(err)
	}
	expired := cursorFixture(c, "q")
	expired.ExpiresAt = time.Now().Add(-time.Second)
	expiredToken, _ := c.signProfileCursor(expired)
	if _, err := c.parseProfileCursor(expiredToken, "q"); !errors.Is(err, ErrCursorExpired) {
		t.Fatal(err)
	}
	restartCtx, stop := context.WithCancel(c.ctx)
	restarted := New(restartCtx, c.Provider, device.NewState("test"), c.store, Options{Profiles: c.options.Profiles, DefaultProfile: c.defaultProfile, CursorSecret: c.cursorSecret})
	defer func() { stop(); restarted.Wait() }()
	// Even retaining the signing key cannot recover a cursor whose snapshot
	// disappeared on restart.
	if _, err := restarted.parseProfileCursor(token, "q"); !errors.Is(err, ErrCursorExpired) {
		t.Fatal(err)
	}

}

func TestCursorCacheEntryAndByteBounds(t *testing.T) {
	t.Run("entries", func(t *testing.T) {
		c, _, _ := profileSetup(t)
		cursor := cursorFixture(c, "q")
		first, _ := c.signProfileCursor(cursor)
		for range maxProfileCursors {
			if _, err := c.signProfileCursor(cursor); err != nil {
				t.Fatal(err)
			}
		}
		if len(c.cursorStates) != maxProfileCursors || len(c.cursorOrder) != maxProfileCursors {
			t.Fatal("cache is not bounded")
		}
		if _, err := c.parseProfileCursor(first, "q"); !errors.Is(err, ErrCursorExpired) {
			t.Fatal("oldest state not evicted", err)
		}
	})
	t.Run("bytes", func(t *testing.T) {
		c, _, _ := profileSetup(t)
		cursor := cursorFixture(c, "q")
		random := rand.New(rand.NewPCG(1, 2))
		title := make([]byte, 1_500_000)
		const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
		for i := range title {
			title[i] = alphabet[random.IntN(len(alphabet))]
		}
		cursor.Profiles["claudecode"].Buffer[0].Title = string(title)
		first, _ := c.signProfileCursor(cursor)
		last := ""
		for range 10 {
			var err error
			last, err = c.signProfileCursor(cursor)
			if err != nil {
				t.Fatal(err)
			}
		}
		if c.cursorBytes > maxProfileCursorBytes || len(c.cursorStates) >= 11 {
			t.Fatalf("byte bound not enforced: %d bytes, %d entries", c.cursorBytes, len(c.cursorStates))
		}
		if _, err := c.parseProfileCursor(first, "q"); !errors.Is(err, ErrCursorExpired) {
			t.Fatal("old state not evicted", err)
		}
		if _, err := c.parseProfileCursor(last, "q"); err != nil {
			t.Fatal(err)
		}
		cursor.Profiles["claudecode"].Buffer[0].Title = strings.Repeat("x", maxProfileCursorState+1)
		_, err := c.signProfileCursor(cursor)
		expectCode(t, err, "catalog_cursor_too_large")
	})
}

func TestCursorCacheConcurrentReadersAndWriters(t *testing.T) {
	c, _, _ := profileSetup(t)
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := range 32 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			q := fmt.Sprint(i)
			token, err := c.signProfileCursor(cursorFixture(c, q))
			if err == nil {
				_, err = c.parseProfileCursor(token, q)
			}
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}
