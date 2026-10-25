package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStageViewRelayAndFailure(t *testing.T) {
	var shown string
	code := 204
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/stage/view" || r.Method != "POST" || r.Header.Get("X-Linxi-Device") != "1" {
			t.Error("bad relay")
		}
		var body struct {
			URL string `json:"url"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		shown = body.URL
		w.WriteHeader(code)
	}))
	defer upstream.Close()
	rt := &runtime{cfg: config{StageURL: upstream.URL + "/stage/?ignored=yes"}}
	if err := rt.setStageView(context.Background(), "https://display.test/task"); err != nil {
		t.Fatal(err)
	}
	if shown != "https://display.test/task" {
		t.Fatal("lost task URL")
	}
	code = 403
	if rt.setStageView(context.Background(), "https://evil.test") == nil {
		t.Fatal("rejected view reported success")
	}
	upstream.Close()
	if rt.setStageView(context.Background(), "") == nil {
		t.Fatal("offline view reported success")
	}
}

func TestStageWakeIsLocalOnly(t *testing.T) {
	rt := &runtime{}
	for _, tc := range []struct {
		remote, origin, key string
		code                int
	}{
		{"192.168.1.2:1234", "", "1", 403},
		{"127.0.0.1:1234", "https://evil.test", "1", 403},
		{"127.0.0.1:1234", "", "", 403},
		{"127.0.0.1:1234", "", "1", 503},
	} {
		req := httptest.NewRequest("POST", "http://127.0.0.1:8788/wake", nil)
		req.RemoteAddr = tc.remote
		req.Header.Set("Origin", tc.origin)
		req.Header.Set("X-Linxi-Device", tc.key)
		w := httptest.NewRecorder()
		rt.wakeHandler().ServeHTTP(w, req)
		if w.Code != tc.code {
			t.Fatalf("%+v: %d", tc, w.Code)
		}
	}
}

func TestPersistentBrowserReuseAndRestart(t *testing.T) {
	// Fake Chromium stays alive without accessing a display or network.
	bin := filepath.Join(t.TempDir(), "browser")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexec sleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	b := &browser{binary: bin, width: 640, height: 480}
	defer b.Stop()
	if err := b.ensureOpen(context.Background(), "http://127.0.0.1/stage/"); err != nil {
		t.Fatal(err)
	}
	first := b.done
	if err := b.ensureOpen(context.Background(), "http://127.0.0.1/stage/"); err != nil {
		t.Fatal(err)
	}
	if first != b.done {
		t.Fatal("wake restarted a live browser")
	}
	b.cancel()
	select {
	case _, open := <-first:
		// Other observers (ensureOpen and Stop) must still see completion
		// after this waiter wakes; a consumable message leaves a race window.
		if open {
			t.Fatal("browser exit notification must be closed for every observer")
		}
	case <-time.After(time.Second):
		t.Fatal("fake browser did not stop")
	}
	if err := b.ensureOpen(context.Background(), "http://127.0.0.1/stage/"); err != nil {
		t.Fatal(err)
	}
	if first == b.done {
		t.Fatal("dead browser not restarted")
	}
	if err := b.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := b.Stop(); err != nil {
		t.Fatal("stop must be idempotent")
	}
	b.binary = "/no/such/browser"
	if b.ensureOpen(context.Background(), "http://127.0.0.1/stage/") == nil {
		t.Fatal("launch failure hidden")
	}
}

func TestCastRejectsInvalidOrDisallowedURLs(t *testing.T) {
	rt := &runtime{cfg: config{AllowedHosts: []string{"display.test"}, AirPlayTarget: "receiver"}}
	for _, raw := range []string{"bad", "://", "file:///tmp/secret", "https://evil.test/"} {
		if rt.CastScreen(context.Background(), raw) == nil {
			t.Fatal("accepted " + raw)
		}
	}
	rt.cfg.AirPlayTarget = ""
	if rt.CastScreen(context.Background(), "https://display.test/") == nil {
		t.Fatal("missing receiver accepted")
	}
	screens, err := rt.ScanScreens(context.Background())
	if err != nil || len(screens) != 0 {
		t.Fatal("unexpected screens")
	}
	rt.cfg.AirPlayTarget = "receiver"
	screens, _ = rt.ScanScreens(context.Background())
	if len(screens) != 1 {
		t.Fatal("missing screen")
	}
}

func TestStageConfigRequiresLoopback(t *testing.T) {
	t.Setenv("AIRPLAY_CREDENTIALS", "")
	t.Setenv("SCREEN_WIDTH", "1920")
	t.Setenv("SCREEN_HEIGHT", "1080")
	for _, raw := range []string{"https://evil.test/stage/", "http://192.168.1.1/stage/", "http://user:pass@127.0.0.1/stage/", "://"} {
		t.Setenv("DEVICE_STAGE_URL", raw)
		if _, err := loadConfig(); err == nil {
			t.Fatal("allowed " + raw)
		}
	}
	t.Setenv("DEVICE_STAGE_URL", "http://127.0.0.1:8090/stage/")
	if _, err := loadConfig(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SCREEN_WIDTH", "100")
	if _, err := loadConfig(); err == nil {
		t.Fatal("invalid dimensions accepted")
	}
}
