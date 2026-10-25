package httpapi

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/wwcchh0123/hackathonGO-2026/internal/artifacts"
	"github.com/wwcchh0123/hackathonGO-2026/internal/config"
)

//go:embed previewui/*
var previewUI embed.FS

var previewPage = template.Must(template.ParseFS(previewUI, "previewui/viewer.html"))

const artifactCSP = "sandbox allow-scripts allow-forms allow-modals allow-downloads; default-src 'self' data: blob:; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; connect-src 'none'; form-action 'none'"

func previewAssets() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name != "viewer.js" && name != "bootstrap.js" {
			http.NotFound(w, r)
			return
		}
		data, err := previewUI.ReadFile("previewui/" + name)
		if err != nil {
			http.Error(w, "preview asset unavailable", 500)
			return
		}
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(data)
	})
}

// A result URL serves a trusted viewer. Only the opaque child loads user HTML;
// it receives its own bounded storage space, never control-origin privileges.
func resultHandler(cfg config.Config) http.Handler {
	files := http.FileServer(http.Dir(cfg.DataDir))
	store := artifacts.Store{Root: cfg.DataDir}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rel := strings.Trim(r.URL.Path, "/")
		parts := strings.SplitN(rel, "/", 2)
		publication := parts[0]
		if !fs.ValidPath(rel) || strings.Contains(rel, "\\") || len(publication) != 32 || strings.Trim(publication, "0123456789abcdef") != "" || strings.EqualFold(filepath.Base(rel), artifacts.ScopeFilename) {
			http.NotFound(w, r)
			return
		}
		info, err := os.Stat(filepath.Join(cfg.DataDir, filepath.FromSlash(rel)))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if info.IsDir() {
			rel += "/index.html"
			info, err = os.Stat(filepath.Join(cfg.DataDir, filepath.FromSlash(rel)))
			if err != nil || info.IsDir() {
				http.NotFound(w, r)
				return
			}
		}
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", artifactCSP)
		if !strings.EqualFold(filepath.Ext(rel), ".html") {
			files.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Query().Get("_linxi_raw") != "1" {
			scope, err := store.StorageScope(publication)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; frame-src 'self'; connect-src 'none'; form-action 'none'")
			_ = previewPage.Execute(w, struct{ Scope string }{scope})
			return
		}
		data, err := os.ReadFile(filepath.Join(cfg.DataDir, filepath.FromSlash(rel)))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		// JSON escaping keeps user </script> and markup inside inert data until
		// the child has installed its isolated storage implementation.
		original, _ := json.Marshal(string(data))
		w.Header().Set("Content-Security-Policy", artifactCSP)
		_, _ = fmt.Fprintf(w, "<!doctype html><html><head><meta charset=\"utf-8\"></head><body><script id=\"linxi-document\" type=\"application/json\">%s</script><script src=\"/preview-assets/bootstrap.js\"></script></body></html>", original)
	})
}
