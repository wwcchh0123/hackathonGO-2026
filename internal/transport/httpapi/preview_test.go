package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/wwcchh0123/hackathonGO-2026/internal/agentapi"
	"github.com/wwcchh0123/hackathonGO-2026/internal/artifacts"
	"github.com/wwcchh0123/hackathonGO-2026/internal/config"
)

func TestResultViewerSeparatesTrustedStorageFromUserHTML(t *testing.T) {
	root := t.TempDir()
	store := artifacts.Store{Root: root}
	original := `<!doctype html><script>window.artifactSentinel="</script><script>alert('user')</script>"</script><form><button>add</button></form>`
	result, err := store.Publish(context.Background(), []agentapi.File{{Path: "index.html", Data: []byte(original)}, {Path: "app.js", Data: []byte("window.loaded=true")}, {Path: "payload", Data: []byte("<html><script>alert(1)</script>")}}, "session-a")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(result)
	mux := http.NewServeMux()
	mux.Handle("GET /results/", http.StripPrefix("/results/", resultHandler(config.Config{DataDir: root})))
	mux.Handle("GET /preview-assets/", http.StripPrefix("/preview-assets/", previewAssets()))
	ts := httptest.NewServer(mux)
	defer ts.Close()
	get := func(path string, want int) (http.Header, string) {
		t.Helper()
		r, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		body, _ := io.ReadAll(r.Body)
		if r.StatusCode != want {
			t.Fatalf("%s: status %d: %s", path, r.StatusCode, body)
		}
		return r.Header, string(body)
	}
	headers, viewer := get(u.Path, 200)
	if strings.Contains(viewer, "artifactSentinel") || !strings.Contains(viewer, "data-storage-scope") || !strings.Contains(viewer, "/preview-assets/viewer.js") {
		t.Fatal("user HTML entered trusted viewer", viewer)
	}
	if strings.Contains(headers.Get("Content-Security-Policy"), "sandbox ") {
		t.Fatal("trusted viewer cannot persist browser state")
	}
	if !strings.Contains(viewer, `sandbox="allow-scripts allow-forms allow-modals allow-downloads"`) {
		t.Fatal("child permissions changed", viewer)
	}
	headers, raw := get(u.Path+"?_linxi_raw=1", 200)
	csp := headers.Get("Content-Security-Policy")
	if strings.Contains(csp, "allow-same-origin") || !strings.Contains(csp, "connect-src 'none'") || !strings.Contains(csp, "form-action 'none'") {
		t.Fatal("artifact isolation weakened", csp)
	}
	if !strings.Contains(csp, "allow-forms") || !strings.Contains(csp, "allow-modals") {
		t.Fatal("client interactions unavailable", csp)
	}
	match := regexp.MustCompile(`(?s)<script id="linxi-document" type="application/json">(.*?)</script>`).FindStringSubmatch(raw)
	var restored string
	if len(match) != 2 || json.Unmarshal([]byte(match[1]), &restored) != nil || restored != original {
		t.Fatal("HTML bootstrap escaped incorrectly", raw)
	}
	if strings.Contains(match[1], "</script>") {
		t.Fatal("user HTML escaped inert JSON")
	}
	base := strings.TrimSuffix(u.Path, "index.html")
	headers, js := get(base+"app.js", 200)
	if js != "window.loaded=true" || headers.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("relative resource unavailable", js)
	}
	headers, _ = get(base+"payload", 200)
	if !strings.HasPrefix(headers.Get("Content-Security-Policy"), "sandbox ") {
		t.Fatal("extensionless HTML can escape sandbox")
	}
	get(base+artifacts.ScopeFilename, 404)
	get("/results/", 404)
	get("/results/"+strings.Repeat("z", 32)+"/index.html", 404)
	get(base+"missing.html", 404)
	get("/preview-assets/viewer.html", 404)
	get("/preview-assets/missing.js", 404)
	for _, asset := range []string{"viewer.js", "bootstrap.js"} {
		h, body := get("/preview-assets/"+asset, 200)
		if !strings.HasPrefix(h.Get("Content-Type"), "text/javascript") || len(body) < 100 {
			t.Fatal("preview runtime unavailable", asset)
		}
	}
	_, fromDirectory := get(base, 200)
	if fromDirectory != viewer {
		t.Fatal("directory and HTML entry have different viewers")
	}
}

func TestResultHandlerRejectsTraversalAndMissingDirectoryEntry(t *testing.T) {
	root := t.TempDir()
	h := resultHandler(config.Config{DataDir: root})
	for _, path := range []string{strings.Repeat("a", 32) + "/../index.html", strings.Repeat("a", 32) + `\..\secret`, strings.Repeat("a", 32) + "/", "invalid/index.html"} {
		r := httptest.NewRecorder()
		h.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/"+path, nil))
		if r.Code != 404 {
			t.Fatal(path, r.Code)
		}
	}
}
