package session

import (
	"context"
	"errors"
	"strings"

	"github.com/wwcchh0123/hackathonGO-2026/internal/device"
)

// Preview is bounded and cached for the current task round. Querying a task
// never downloads its files; the public display page calls this method only
// after the session becomes idle.
func (c *Controller) Preview(ctx context.Context, ref string) (string, error) {
	select {
	case c.previewing <- struct{}{}:
		defer func() { <-c.previewing }()
	default:
		return "", errors.New("预览下载已达并发上限")
	}
	s, err := c.SessionFor(ctx, ref)
	if err != nil {
		return "", err
	}
	if s.Status != "idle" {
		return "", errors.New("运行中任务的产物可能变化，请空闲后预览")
	}
	c.mu.Lock()
	t, err := c.recordLocked(ref)
	if err != nil {
		c.mu.Unlock()
		return "", err
	}
	// A display page polls while an idle session settles. Reuse the published
	// URL so those polls do not download the same export repeatedly. A new
	// command increments generation and clears resultURL before it starts.
	if t.resultURL != "" && c.store.Available(t.resultURL) {
		result := t.resultURL
		c.mu.Unlock()
		return result, nil
	}
	generation := t.generation
	viewGeneration := c.viewGeneration
	c.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, c.options.PublishTimeout)
	defer cancel()
	files, err := c.Provider.Artifacts(ctx, s.ID)
	if err != nil {
		return "", err
	}
	html := false
	for _, f := range files {
		if strings.HasSuffix(f.Path, ".html") {
			html = true
		}
	}
	if !html {
		return "", errors.New("云端没有可预览的 HTML 导出文件；其他文件仍可在任务详情查看")
	}
	result, err := c.store.Publish(ctx, files, s.ID)
	if err != nil {
		return "", err
	}
	current, err := c.status(ctx, s.ID)
	c.mu.Lock()
	valid := err == nil && current.Status == "idle" && t.generation == generation && ctx.Err() == nil
	if valid {
		t.resultURL = result
		if c.State.Get().TaskRef == ref && c.viewGeneration == viewGeneration {
			c.State.Update(func(s *device.Snapshot) { s.ResultURL = result; s.PreviewError = "" })
		}
	}
	c.mu.Unlock()
	if !valid {
		_ = c.store.Discard(result)
		return "", errors.New("预览期间任务已变化，请重新读取")
	}
	return result, nil
}
