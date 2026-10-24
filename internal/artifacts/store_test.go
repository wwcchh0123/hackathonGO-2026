package artifacts

import (
	"context"
	"strings"
	"testing"

	"github.com/wwcchh0123/hackathonGO-2026/internal/agentapi"
)

func TestPublishRejectsEscapingArtifactPaths(t *testing.T) {
	store := Store{Root: t.TempDir(), PublicURL: "http://example.test"}
	for _, p := range []string{"../index.html", "/etc/index.html", `..\index.html`} {
		if _, err := store.Publish(context.Background(), []agentapi.File{{Path: p}}); err == nil {
			t.Errorf("accepted unsafe path %q", p)
		}
	}
	url, err := store.Publish(context.Background(), []agentapi.File{{Path: "index.html", Data: []byte("hello")}})
	if err != nil || !strings.HasPrefix(url, "http://example.test/results/") {
		t.Fatalf("publish: %s %v", url, err)
	}
}
