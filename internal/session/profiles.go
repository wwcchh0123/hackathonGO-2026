package session

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wwcchh0123/hackathonGO-2026/internal/agentapi"
)

const (
	profileCursorTTL      = 10 * time.Minute
	maxProfileCursors     = 256
	maxProfileCursorBytes = 8 << 20
	maxProfileCursorState = 2 << 20
)

type cachedProfileCursor struct {
	data    []byte
	expires time.Time
}

type profileCursorEntry struct {
	Buffer   []agentapi.Session `json:"lookahead,omitempty"`
	NextPage string             `json:"next_page,omitempty"`
	Done     bool               `json:"done"`
}
type profileCursor struct {
	Fingerprint string                        `json:"fingerprint"`
	Generation  string                        `json:"generation"`
	Snapshot    time.Time                     `json:"snapshot"`
	ExpiresAt   time.Time                     `json:"expires_at"`
	Profiles    map[string]profileCursorEntry `json:"profiles"`
}

func (c *Controller) profileFingerprint(o ListOptions) string {
	o.Page = ""
	o.SkipSummary = false
	raw, _ := json.Marshal(struct {
		Query   ListOptions
		Device  string
		Unowned bool
	}{o, c.deviceID, c.includeUnowned})
	hash := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(hash[:])
}
func (c *Controller) signProfileCursor(cursor profileCursor) (string, error) {
	raw, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	if len(raw) > maxProfileCursorState {
		return "", business("catalog_cursor_too_large", "", "", "narrow the runtime or time filters")
	}
	var compressed bytes.Buffer
	zipper := zlib.NewWriter(&compressed)
	_, _ = zipper.Write(raw)
	_ = zipper.Close()
	data := compressed.Bytes()
	id := agentapi.ID()
	c.mu.Lock()
	c.pruneProfileCursorsLocked(time.Now())
	for len(c.cursorStates) >= maxProfileCursors || c.cursorBytes+len(data) > maxProfileCursorBytes {
		oldest := c.cursorOrder[0]
		c.cursorOrder = c.cursorOrder[1:]
		c.cursorBytes -= len(c.cursorStates[oldest].data)
		delete(c.cursorStates, oldest)
	}
	c.cursorStates[id] = cachedProfileCursor{data: data, expires: cursor.ExpiresAt}
	c.cursorOrder = append(c.cursorOrder, id)
	c.cursorBytes += len(data)
	c.mu.Unlock()
	// Only a random state reference is sent over HTTP/MCP. The signed snapshot
	// remains immutable and bounded in memory, independent of lookahead size.
	ref := "v2." + id
	sig := hmac.New(sha256.New, c.cursorSecret)
	_, _ = sig.Write([]byte(ref))
	return ref + "." + base64.RawURLEncoding.EncodeToString(sig.Sum(nil)), nil
}

func (c *Controller) pruneProfileCursorsLocked(now time.Time) {
	order := c.cursorOrder[:0]
	for _, id := range c.cursorOrder {
		entry := c.cursorStates[id]
		if !now.Before(entry.expires) {
			c.cursorBytes -= len(entry.data)
			delete(c.cursorStates, id)
			continue
		}
		order = append(order, id)
	}
	c.cursorOrder = order
}

func (c *Controller) parseProfileCursor(raw, fingerprint string) (profileCursor, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 || parts[0] != "v2" || len(parts[1]) != 32 {
		return profileCursor{}, ErrCursorExpired
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return profileCursor{}, ErrCursorExpired
	}
	sig := hmac.New(sha256.New, c.cursorSecret)
	_, _ = sig.Write([]byte(parts[0] + "." + parts[1]))
	if !hmac.Equal(sig.Sum(nil), signature) {
		return profileCursor{}, ErrCursorExpired
	}
	c.mu.Lock()
	c.pruneProfileCursorsLocked(time.Now())
	state, ok := c.cursorStates[parts[1]]
	c.mu.Unlock()
	if !ok {
		return profileCursor{}, ErrCursorExpired
	}
	unzip, err := zlib.NewReader(bytes.NewReader(state.data))
	if err != nil {
		return profileCursor{}, ErrCursorExpired
	}
	defer unzip.Close()
	plain, err := io.ReadAll(io.LimitReader(unzip, maxProfileCursorState+1))
	if err != nil || len(plain) > maxProfileCursorState {
		return profileCursor{}, ErrCursorExpired
	}
	var cursor profileCursor
	if json.Unmarshal(plain, &cursor) != nil || cursor.Profiles == nil || cursor.Fingerprint != fingerprint || cursor.Generation != c.profileGeneration() || time.Now().After(cursor.ExpiresAt) {
		return profileCursor{}, ErrCursorExpired
	}
	return cursor, nil
}
func (c *Controller) profileGeneration() string {
	keys := make([]string, 0, len(c.bindings))
	for key := range c.bindings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var profiles []any
	for _, key := range keys {
		profiles = append(profiles, c.bindings[key].Profile)
	}
	raw, _ := json.Marshal(profiles)
	hash := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(hash[:])
}
func (c *Controller) profileBindings(runtime string) []ProfileBinding {
	out := make([]ProfileBinding, 0, len(c.bindings))
	for _, binding := range c.bindings {
		if !binding.Profile.ReadEnabled || binding.Provider == nil {
			continue
		}
		if runtime != "" && runtime != binding.Profile.Runtime {
			continue
		}
		out = append(out, binding)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Profile.Key < out[j].Profile.Key })
	return out
}

// Each profile retains a bounded validated lookahead page and upstream cursor. This
// avoids skipping records when only some of a profile's page wins the merge.
func (c *Controller) fillProfileHead(ctx context.Context, binding ProfileBinding, o ListOptions, snapshot time.Time, entry *profileCursorEntry) error {
	for scanned := 0; len(entry.Buffer) == 0 && !entry.Done; scanned++ {
		if scanned >= 256 {
			return business("catalog_scan_limit", "", "", "narrow the time or runtime filters")
		}
		before := snapshot.Format(time.RFC3339Nano)
		if o.CreatedBefore != "" {
			t, _ := time.Parse(time.RFC3339Nano, o.CreatedBefore)
			if t.Before(snapshot) {
				before = o.CreatedBefore
			}
		}
		result, err := binding.Provider.ListSessions(ctx, agentapi.SessionQuery{Limit: 50, Page: entry.NextPage, AgentID: binding.Profile.AgentID, Status: o.Status, IncludeArchived: o.IncludeArchived, CreatedAfter: o.CreatedAfter, CreatedBefore: before})
		if err != nil {
			return err
		}
		if result.NextPage != "" && result.NextPage == entry.NextPage {
			return business("profile_inconsistent", "", "", "check upstream pagination")
		}
		entry.NextPage = result.NextPage
		entry.Done = result.NextPage == ""
		// Providers must obey Limit; excess records must not be silently dropped.
		if len(result.Data) > 50 {
			return business("profile_inconsistent", "", "", "check upstream page limit")
		}
		if len(result.Data) == 0 {
			continue
		}
		for _, s := range result.Data {
			if !matchesProfile(s, binding) {
				return business("profile_inconsistent", s.Runtime, binding.Profile.Runtime, "check the configured Agent version")
			}
			if !c.ownsSession(s) || s.CreatedAt.After(snapshot) {
				continue
			}
			if o.Model.ID != "" && (s.Model.ID != o.Model.ID || (o.Model.Effort != "" && s.Model.Effort != o.Model.Effort)) {
				continue
			}
			entry.Buffer = append(entry.Buffer, s)
		}
		sort.SliceStable(entry.Buffer, func(i, j int) bool { return sessionBefore(entry.Buffer[i], entry.Buffer[j]) })
	}
	return nil
}
func matchesProfile(s agentapi.Session, b ProfileBinding) bool {
	p := b.Profile
	return s.ID != "" && s.AgentID == p.AgentID && s.AgentVersion == p.AgentVersion && s.Runtime == p.Runtime && s.EnvironmentID == p.EnvironmentID
}
func sessionBefore(a, b agentapi.Session) bool {
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.After(b.CreatedAt)
	}
	if a.ID != b.ID {
		return a.ID > b.ID
	}
	return a.Runtime < b.Runtime
}
func (c *Controller) listProfileTasks(ctx context.Context, o ListOptions) (TaskPage, error) {
	if o.AgentID != "" {
		return TaskPage{}, business("invalid_filter", "", o.AgentID, "filter by runtime instead of Agent ID")
	}
	bindings := c.profileBindings(o.Runtime)
	if len(bindings) == 0 {
		return TaskPage{}, business("runtime_unavailable", "", o.Runtime, "list_tasks without runtime")
	}
	ctx, cancel := context.WithTimeout(ctx, c.options.RequestTimeout)
	defer cancel()
	fingerprint := c.profileFingerprint(o)
	cursor := profileCursor{Fingerprint: fingerprint, Generation: c.profileGeneration(), Snapshot: time.Now().UTC(), ExpiresAt: time.Now().Add(profileCursorTTL), Profiles: map[string]profileCursorEntry{}}
	if o.Page != "" {
		var err error
		cursor, err = c.parseProfileCursor(o.Page, fingerprint)
		if err != nil {
			return TaskPage{}, err
		}
	}
	page := TaskPage{Data: []Task{}, Sort: "created_at_desc", Scope: "runtime_profiles", QueriedAt: time.Now().UTC(), Filters: o, Complete: true}
	failed := map[string]bool{}
	acceptHead := func(b ProfileBinding, entry profileCursorEntry, err error) {
		key := b.Profile.Key
		if err != nil {
			failed[key] = true
			page.Partial = true
			page.Complete = false
			page.Errors = append(page.Errors, TaskError{Runtime: b.Profile.Runtime, Code: providerCode(err), Retryable: agentapi.Retryable(err) || errors.Is(err, context.DeadlineExceeded)})
		}
		cursor.Profiles[key] = entry
	}
	fill := func(b ProfileBinding) {
		if failed[b.Profile.Key] {
			return
		}
		entry := cursor.Profiles[b.Profile.Key]
		err := c.fillProfileHead(ctx, b, o, cursor.Snapshot, &entry)
		acceptHead(b, entry, err)
	}
	// Registry validation caps this at 16 concurrent profile reads. Starting
	// every head before waiting prevents one timeout from starving healthy
	// profiles of the shared page budget.
	type headResult struct {
		entry profileCursorEntry
		err   error
	}
	heads := make([]headResult, len(bindings))
	var wg sync.WaitGroup
	for i, b := range bindings {
		wg.Add(1)
		go func(i int, b ProfileBinding) {
			defer wg.Done()
			entry := cursor.Profiles[b.Profile.Key]
			err := c.fillProfileHead(ctx, b, o, cursor.Snapshot, &entry)
			heads[i] = headResult{entry: entry, err: err}
		}(i, b)
	}
	wg.Wait()
	for i, b := range bindings {
		result := heads[i]
		acceptHead(b, result.entry, result.err)
	}
	for len(page.Data) < o.Limit {
		var winner *ProfileBinding
		for i := range bindings {
			b := &bindings[i]
			entry := cursor.Profiles[b.Profile.Key]
			if failed[b.Profile.Key] || len(entry.Buffer) == 0 {
				continue
			}
			if winner == nil || sessionBefore(entry.Buffer[0], cursor.Profiles[winner.Profile.Key].Buffer[0]) {
				winner = b
			}
		}
		if winner == nil {
			break
		}
		entry := cursor.Profiles[winner.Profile.Key]
		s := entry.Buffer[0]
		entry.Buffer = entry.Buffer[1:]
		cursor.Profiles[winner.Profile.Key] = entry
		c.mu.Lock()
		ref, err := c.referenceLockedFor(s, *winner)
		c.mu.Unlock()
		if err != nil {
			return TaskPage{}, err
		}
		task := taskCard(s, ref)
		task.ProfileKey = winner.Profile.Key
		if !winner.Profile.Capabilities.Continue {
			task.CanContinue = false
			task.ContinueReason = "runtime_not_capable"
		}
		if !winner.Profile.Capabilities.Events {
			task.SummaryUnavailable = true
		}
		if !o.SkipSummary && winner.Profile.Capabilities.Events {
			task.Summary, task.SummaryUnavailable = c.summary(ctx, winner.Provider, s.ID)
		}
		page.Data = append(page.Data, task)
		if len(page.Data) < o.Limit {
			fill(*winner)
		}
	}
	hasMore := false
	for _, entry := range cursor.Profiles {
		if len(entry.Buffer) > 0 || !entry.Done {
			hasMore = true
		}
	}
	if hasMore && !page.Partial {
		next, err := c.signProfileCursor(cursor)
		if err != nil {
			return TaskPage{}, err
		}
		page.NextPage = next
		page.HasMore = true
	}
	return page, nil
}
func providerCode(err error) string {
	var businessErr *Error
	if errors.As(err, &businessErr) {
		return businessErr.Code
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "provider_timeout"
	}
	var providerErr *agentapi.Error
	if errors.As(err, &providerErr) && providerErr.StatusCode != 0 {
		return fmt.Sprintf("provider_http_%d", providerErr.StatusCode)
	}
	return "provider_unavailable"
}
