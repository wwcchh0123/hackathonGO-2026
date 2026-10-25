// Package artifacts hosts exported static web files, not arbitrary dev servers.
package artifacts

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/wwcchh0123/hackathonGO-2026/internal/agentapi"
)

type Store struct {
	Root, PublicURL string
	mu              sync.Mutex
}

func (s *Store) Publish(ctx context.Context, files []agentapi.File) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(files) == 0 {
		return "", nil
	}
	token := agentapi.ID()
	root := filepath.Join(s.Root, token)
	entry := ""
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(root)
		}
	}()
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		p := path.Clean(strings.ReplaceAll(f.Path, "\\", "/"))
		if p == "." || strings.HasPrefix(p, "../") || path.IsAbs(p) || strings.Contains(p, ":") {
			return "", fmt.Errorf("unsafe artifact path")
		}
		if p == "index.html" {
			entry = p
		} else if entry == "" && strings.HasSuffix(p, ".html") {
			entry = p
		}
		file := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
			return "", err
		}
		if err := os.WriteFile(file, f.Data, 0644); err != nil {
			return "", err
		}
	}
	if entry == "" {
		return "", fmt.Errorf("no HTML entry in exported artifacts")
	}
	complete = true
	entries, _ := os.ReadDir(s.Root)
	// Only completed publications use 32-hex names. Keep the latest 32, bounding
	// disk usage across historical tasks and repeated explicit preview requests.
	type publication struct {
		name string
		time int64
	}
	var ready []publication
	for _, e := range entries {
		if e.IsDir() && len(e.Name()) == 32 && strings.Trim(e.Name(), "0123456789abcdef") == "" && e.Name() != token {
			if info, err := e.Info(); err == nil {
				ready = append(ready, publication{e.Name(), info.ModTime().UnixNano()})
			}
		}
	}
	sort.Slice(ready, func(i, j int) bool { return ready[i].time > ready[j].time })
	for i := 31; i < len(ready); i++ {
		_ = os.RemoveAll(filepath.Join(s.Root, ready[i].name))
	}
	resultPath := (&url.URL{Path: "/results/" + token + "/" + entry}).EscapedPath()
	resultURL, err := url.Parse(strings.TrimRight(s.PublicURL, "/") + resultPath)
	if err != nil {
		return "", err
	}
	if isNgrokHost(resultURL.Hostname()) {
		query := resultURL.Query()
		// ngrok's free browser interstitial also applies to nested iframe
		// navigations, so the published HTML URL needs the bypass itself.
		query.Set("ngrok-skip-browser-warning", "true")
		resultURL.RawQuery = query.Encode()
	}
	return resultURL.String(), nil
}

func isNgrokHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return strings.HasSuffix(host, ".ngrok-free.dev") || strings.HasSuffix(host, ".ngrok.app") || strings.HasSuffix(host, ".ngrok.io")
}

// Available lets an evicted local preview fall back to an explicit fresh fetch.
func (s *Store) Available(result string) bool {
	u, err := url.Parse(result)
	if err != nil {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/results/"), "/")
	if !strings.HasPrefix(u.Path, "/results/") || len(parts) < 2 || len(parts[0]) != 32 || strings.Trim(parts[0], "0123456789abcdef") != "" {
		return false
	}
	p := path.Clean(strings.Join(parts[1:], "/"))
	if p == "." || strings.HasPrefix(p, "../") || path.IsAbs(p) {
		return false
	}
	info, err := os.Stat(filepath.Join(s.Root, parts[0], filepath.FromSlash(p)))
	return err == nil && !info.IsDir()
}

// Discard removes a completed publication that lost its session/turn race.
func (s *Store) Discard(result string) error {
	u, err := url.Parse(result)
	if err != nil {
		return err
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/results/"), "/")
	if !strings.HasPrefix(u.Path, "/results/") || len(parts) < 2 || len(parts[0]) != 32 {
		return fmt.Errorf("invalid publication")
	}
	for _, r := range parts[0] {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return fmt.Errorf("invalid publication")
		}
	}
	return os.RemoveAll(filepath.Join(s.Root, parts[0]))
}
