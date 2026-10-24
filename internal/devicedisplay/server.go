package devicedisplay

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed web/*
var webFiles embed.FS

// WebFS returns the embedded session display assets. The control service
// serves these files from its public /display/ route, while the simulated
// device does not need to host a second copy of the renderer.
func WebFS() fs.FS {
	web, err := fs.Sub(webFiles, "web")
	if err != nil {
		panic(err)
	}
	return web
}

// StaticHandler is useful for tests and for local development of the page.
func StaticHandler() http.Handler {
	return http.FileServer(http.FS(WebFS()))
}
