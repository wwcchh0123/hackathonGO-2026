package artifacts

import (
	"context"
	"os"
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

func TestPublishAddsNgrokBrowserBypass(t *testing.T) {
	store := Store{Root: t.TempDir(), PublicURL: "https://demo.ngrok-free.dev"}
	result, err := store.Publish(context.Background(), []agentapi.File{{Path: "index.html", Data: []byte("hello")}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "ngrok-skip-browser-warning=true") {
		t.Fatalf("missing ngrok browser bypass: %s", result)
	}
}

func TestPublicationsAreBoundedAndDiscardValidated(t *testing.T) {
	store := Store{Root: t.TempDir(), PublicURL: "http://example.test"}
	var result string
	for range 34 {
		var err error
		result, err = store.Publish(context.Background(), []agentapi.File{{Path: "index.html", Data: []byte("ok")}})
		if err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(store.Root)
	if err != nil || len(entries) > 32 {
		t.Fatal(len(entries), err)
	}
	if err := store.Discard(result); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"http://example.test/", "/results/../../etc/file", "/results/zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz/file"} {
		if store.Discard(invalid) == nil {
			t.Fatal("invalid discard accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Publish(ctx, []agentapi.File{{Path: "index.html"}}); err == nil {
		t.Fatal("ignored cancellation")
	}
	if _, err := store.Publish(context.Background(), []agentapi.File{{Path: "notes.txt"}}); err == nil {
		t.Fatal("published without HTML")
	}
}
