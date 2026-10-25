package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/wwcchh0123/hackathonGO-2026/internal/devicecontrol"
)

// Run against an isolated Chromium instance. The fixture reproduces the real
// layout: a same-origin viewer containing an opaque sandboxed artifact. It also
// uses a delegated click on an ordinary div with no button role/onclick, so a
// button-only projection cannot pass this test. No cloud/AirPlay is involved.
func TestBrowserDOMWithChromium(t *testing.T) {
	binary := os.Getenv("LINXI_BROWSER_TEST_BIN")
	if binary == "" {
		t.Skip("set LINXI_BROWSER_TEST_BIN to test real Chromium")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/":
			fmt.Fprint(w, `<!doctype html><title>Display fixture</title><div id="card" class="expand-card" style="padding:20px;background:#ddd">展开卡片</div><button id="change">替换卡片</button><div id="hidden" hidden>隐藏控件</div><iframe title="Viewer" src="/viewer" style="display:block;margin:20px;width:600px;height:480px"></iframe><script>document.body.addEventListener('click',e=>{if(e.target.id==='card'){e.target.textContent=e.isTrusted?'已展开':'不可信点击'}if(e.target.id==='change'){document.getElementById('card').outerHTML='<div id="card">新节点</div>'}})</script>`)
		case "/viewer":
			fmt.Fprint(w, `<!doctype html><title>Viewer fixture</title><iframe title="Artifact" sandbox="allow-scripts" src="/artifact" style="border:0;width:560px;height:400px"></iframe>`)
		case "/artifact":
			fmt.Fprint(w, `<!doctype html><title>Artifact fixture</title><div style="padding:40px"><span id="time">25:00</span><div id="play" class="play-control" tabindex="0" style="margin-top:20px;padding:24px;background:blue;color:white">播放</div><button disabled>不可点击</button><canvas width="10" height="10"></canvas></div><script>document.body.addEventListener('click',e=>{if(e.target.id==='play'&&e.isTrusted){e.target.textContent='暂停';document.getElementById('time').textContent='24:59'}})</script>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	cmd := exec.CommandContext(ctx, binary, "--headless=new", "--no-sandbox", "--disable-gpu", "--no-first-run", "--disable-dev-shm-usage", "--site-per-process", "--user-data-dir="+t.TempDir(), "--window-size=1280,1000", "--remote-debugging-port="+strconv.Itoa(port), server.URL)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	b := &browser{debugPort: port, width: 1280, height: 1000}
	defer func() {
		// Graceful browser shutdown waits for renderers to stop writing their
		// temporary profile before Go's TempDir cleanup removes the directory.
		shutdown, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		if page, err := b.page(shutdown); err == nil {
			_ = b.command(shutdown, page, 1, "Browser.close", nil, nil)
		}
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-shutdown.Done():
			cancel()
			<-done
		}
	}()
	var snapshot devicecontrol.BrowserInspection
	for {
		snapshot, err = b.Inspect(ctx)
		if err == nil && findDOM(snapshot, "div", "播放") != "" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("frames not ready: %v %+v", err, snapshot)
		case <-time.After(100 * time.Millisecond):
		}
	}
	var html string
	for _, f := range snapshot.Frames {
		html += f.HTML
	}
	for _, expected := range []string{`class="expand-card"`, `class="play-control"`, "<iframe", "<canvas", "data-element-id="} {
		if !strings.Contains(html, expected) {
			t.Fatalf("missing DOM structure %q: %s", expected, html)
		}
	}
	if strings.Contains(html, "addEventListener") || strings.Contains(html, "隐藏控件") {
		t.Fatal("hidden/script content leaked into structure")
	}
	play := findDOM(snapshot, "div", "播放")
	oldCard := findDOM(snapshot, "div", "展开卡片")
	if err := b.Click(ctx, play); err != nil {
		t.Fatal("iframe click:", err)
	}
	snapshot, err = b.Inspect(ctx)
	if err != nil || findDOM(snapshot, "div", "暂停") == "" || findDOM(snapshot, "span", "24:59") == "" {
		t.Fatalf("iframe did not receive real click: %v %+v", err, snapshot)
	}
	if err := b.Click(ctx, oldCard); err == nil {
		t.Fatal("old snapshot ID reused")
	}
	card := findDOM(snapshot, "div", "展开卡片")
	if err := b.Click(ctx, card); err != nil {
		t.Fatal("non-button click:", err)
	}
	snapshot, err = b.Inspect(ctx)
	if err != nil || findDOM(snapshot, "div", "已展开") == "" {
		t.Fatalf("delegated div click failed: %v %+v", err, snapshot)
	}
	for _, e := range snapshot.Elements {
		if e.Disabled && b.Click(ctx, e.ID) == nil {
			t.Fatal("disabled element clicked")
		}
	}
	detached := findDOM(snapshot, "div", "已展开")
	if err := b.Click(ctx, findDOM(snapshot, "button", "替换卡片")); err != nil {
		t.Fatal(err)
	}
	if err := b.Click(ctx, detached); err == nil {
		t.Fatal("detached node clicked")
	}
	capture, err := b.Capture(ctx)
	if err != nil || capture.Inspection == nil || !strings.HasPrefix(string(capture.PNG), "\x89PNG") || len(capture.Inspection.Frames) < 3 {
		t.Fatalf("image+DOM capture: %v structure_error=%q", err, capture.StructureError)
	}
}

func findDOM(snapshot devicecontrol.BrowserInspection, tag, text string) string {
	for _, e := range snapshot.Elements {
		if e.Tag == tag && (e.Text == text || e.Label == text) {
			return e.ID
		}
	}
	return ""
}
