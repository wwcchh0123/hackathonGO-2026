// Package artifacts hosts exported static web files, not arbitrary dev servers.
package artifacts

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/wwcchh0123/hackathonGO-2026/internal/agentapi"
)

type Store struct{ Root, PublicURL string }

func (s *Store) Publish(ctx context.Context, files []agentapi.File) (string, error) {
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
	resultPath := (&url.URL{Path: "/results/" + token + "/" + entry}).EscapedPath()
	return strings.TrimRight(s.PublicURL, "/") + resultPath, nil
}
