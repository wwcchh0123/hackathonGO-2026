package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/wwcchh0123/hackathonGO-2026/internal/agentapi"
	"github.com/wwcchh0123/hackathonGO-2026/internal/device"
)

func fingerprint(files []agentapi.File) string {
	sorted := append([]agentapi.File(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	h := sha256.New()
	for _, f := range sorted {
		fmt.Fprintf(h, "%d:%s:%d:", len(f.Path), f.Path, len(f.Data))
		h.Write(f.Data)
	}
	return hex.EncodeToString(h.Sum(nil))
}
func (c *Controller) publicationCurrent(id string, sessionGen, turnGen uint64, terminal string) bool {
	return c.sessionGeneration == sessionGen && c.turnGeneration == turnGen && c.current.terminal == terminal && c.State.Get().SessionID == id
}
func (c *Controller) publish(ctx context.Context, cancel context.CancelFunc, id string, sessionGen, turnGen uint64, terminal string) {
	defer c.wg.Done()
	defer cancel()
	var files []agentapi.File
	var err error
	// Files may become visible shortly after the terminal event.
	for attempt := 0; attempt < 5; attempt++ {
		var state agentapi.Session
		state, err = c.status(ctx, id)
		if err == nil && state.Status != "idle" {
			return
		}
		if err == nil {
			files, err = c.Provider.Artifacts(ctx, id)
		}
		hasHTML := false
		for _, f := range files {
			if strings.HasSuffix(f.Path, ".html") {
				hasHTML = true
			}
		}
		if err != nil || hasHTML || attempt == 4 {
			break
		}
		timer := time.NewTimer(500 * time.Millisecond * time.Duration(1<<attempt))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
	if ctx.Err() != nil {
		return
	}
	digest := fingerprint(files)
	c.mu.Lock()
	current := c.publicationCurrent(id, sessionGen, turnGen, terminal)
	unchanged := c.published[id] == digest
	c.mu.Unlock()
	if !current {
		return
	}
	hasHTML := false
	for _, f := range files {
		if strings.HasSuffix(f.Path, ".html") {
			hasHTML = true
		}
	}
	var result string
	if err == nil && hasHTML && !unchanged {
		result, err = c.store.Publish(ctx, files)
	}
	c.mu.Lock()
	if !c.publicationCurrent(id, sessionGen, turnGen, terminal) || ctx.Err() != nil {
		c.mu.Unlock()
		if result != "" {
			_ = c.store.Discard(result)
		}
		return
	}
	c.State.Update(func(s *device.Snapshot) {
		switch {
		case err != nil:
			s.PreviewError = "发布预览失败：" + err.Error()
		case !hasHTML:
			s.Summary = "本轮已结束，暂无可展示页面"
		case unchanged:
			s.Summary = "本轮已结束，未检测到新的预览内容"
		default:
			s.ResultURL = result
			s.PreviewError = ""
			c.published[id] = digest
		}
	})
	c.mu.Unlock()
	slog.Info("preview publication finished", "session_id", id, "terminal_event_id", terminal, "published", result != "", "unchanged", unchanged, "error", err)
}
