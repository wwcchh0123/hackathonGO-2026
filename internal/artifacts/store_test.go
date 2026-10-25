package artifacts

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
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

func TestPublishPreservesCompleteProjectTreeAndUsesRootIndex(t *testing.T) {
	store := Store{Root: t.TempDir(), PublicURL: "http://example.test"}
	files := []agentapi.File{
		{Path: "index.html", Data: []byte(`<script src="assets/app.js"></script><link rel="stylesheet" href="assets/app.css"><img src="images/logo.svg">`)},
		{Path: "assets/app.js", Data: []byte("console.log('ready')")},
		{Path: "assets/app.css", Data: []byte("body { color: green; }")},
		{Path: "images/logo.svg", Data: []byte("<svg viewBox=\"0 0 1 1\"></svg>")},
		{Path: "data/example.json", Data: []byte(`{"ready":true}`)},
	}
	result, err := store.Publish(context.Background(), files, "session-complete-project")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(result)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/results/"), "/")
	if len(parts) != 2 || parts[1] != "index.html" {
		t.Fatalf("preview entry is not root index.html: %s", result)
	}
	publication := filepath.Join(store.Root, parts[0])
	for _, file := range files {
		got, err := os.ReadFile(filepath.Join(publication, filepath.FromSlash(file.Path)))
		if err != nil {
			t.Fatalf("missing artifact %s: %v", file.Path, err)
		}
		if string(got) != string(file.Data) {
			t.Fatalf("artifact %s changed during publication", file.Path)
		}
	}
}

func TestStorageScopeIsBoundToTaskAndSurvivesCacheReload(t *testing.T) {
	store := Store{Root: t.TempDir(), PublicURL: "http://example.test"}
	publish := func(scope string) (string, string) {
		t.Helper()
		result, err := store.Publish(context.Background(), []agentapi.File{{Path: "index.html", Data: []byte("app")}}, scope)
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(result)
		publication := strings.Split(strings.TrimPrefix(u.Path, "/results/"), "/")[0]
		reloaded := Store{Root: store.Root}
		value, err := reloaded.StorageScope(publication)
		if err != nil {
			t.Fatal(err)
		}
		return publication, value
	}
	first, a := publish("session-a")
	_, next := publish("session-a")
	_, b := publish("session-b")
	if a != next || a == b || strings.Contains(a, "session") {
		t.Fatal("task scope isolation failed", a, next, b)
	}
	if err := os.Remove(filepath.Join(store.Root, first, ScopeFilename)); err != nil {
		t.Fatal(err)
	}
	legacy, err := store.StorageScope(first)
	if err != nil || len(legacy) != 64 {
		t.Fatal("legacy publication unavailable", err)
	}
	if err := os.WriteFile(filepath.Join(store.Root, first, ScopeFilename), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StorageScope(first); err == nil {
		t.Fatal("accepted corrupt scope")
	}
	for _, invalid := range []string{"../outside", strings.Repeat("z", 32), strings.Repeat("a", 32)} {
		if _, err := store.StorageScope(invalid); err == nil {
			t.Fatal("accepted unavailable publication", invalid)
		}
	}
}

func TestPublicationScopeCannotBeSuppliedByArtifact(t *testing.T) {
	store := Store{Root: t.TempDir()}
	for _, name := range []string{ScopeFilename, strings.ToUpper(ScopeFilename), ScopeFilename + "/index.html"} {
		if _, err := store.Publish(context.Background(), []agentapi.File{{Path: "index.html"}, {Path: name, Data: []byte("another-task")}}, "task"); err == nil {
			t.Fatal("artifact controlled publication metadata", name)
		}
	}
	entries, _ := os.ReadDir(store.Root)
	if len(entries) != 0 {
		t.Fatal("failed publication retained", len(entries))
	}
	if _, err := store.Publish(context.Background(), []agentapi.File{{Path: "a", Data: []byte("file")}, {Path: "a/index.html"}}); err == nil {
		t.Fatal("accepted conflicting file/directory export")
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
