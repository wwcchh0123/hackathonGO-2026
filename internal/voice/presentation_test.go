package voice

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPresentationLifecycleAndStalePlayback(t *testing.T) {
	var h stageHub
	if h.snapshot().Phase != "offline" {
		t.Fatal("not offline initially")
	}
	h.update(func(p *Presentation) { p.ViewURL = "https://display.test/display/" })
	h.reset("idle", true)
	h.reset("listening", true)
	turn := h.reset("thinking", true)
	h.playback(turn, "playing", .6, "你好")
	if p := h.snapshot(); p.Phase != "speaking" || p.Text != "你好" || p.Level != .6 {
		t.Fatalf("bad playback: %+v", p)
	}
	h.playback(turn, "buffering", 0, "")
	if p := h.snapshot(); p.Phase != "speaking" || p.Level != 0 {
		t.Fatal("buffering must close mouth but preserve speech")
	}
	h.playback(turn, "idle", 0, "")
	if h.snapshot().Phase != "idle" {
		t.Fatal("playback completion not applied")
	}
	h.reset("listening", true)
	h.playback(turn, "playing", 1, "旧回复")
	if h.snapshot().Phase != "listening" {
		t.Fatal("old speech overrode listening")
	}
	newTurn := h.reset("thinking", true)
	h.playback(turn, "idle", 0, "")
	if h.snapshot().Phase != "thinking" {
		t.Fatal("old completion overrode new reply")
	}
	h.playback(newTurn, "playing", 100, strings.Repeat("你", 1000))
	if p := h.snapshot(); p.Level != 1 || len([]rune(p.Text)) != 600 {
		t.Fatal("unbounded display input")
	}
	h.reset("offline", false)
	h.playback(newTurn, "playing", 1, "旧回复")
	if p := h.snapshot(); p.Connected || p.Level != 0 || p.ViewURL == "" {
		t.Fatal("disconnect must clear speech and keep task view")
	}
}

func TestStageSlowSubscriberAndCapacity(t *testing.T) {
	var h stageHub
	ch, cancel, ok := h.subscribe()
	if !ok {
		t.Fatal("subscribe")
	}
	defer cancel()
	for i := 0; i < 100; i++ {
		h.reset("idle", true)
	}
	if p := <-ch; p.Turn != 100 {
		t.Fatalf("screen did not receive latest state: %d", p.Turn)
	}
	var cancellations []func()
	for i := 0; i < 7; i++ {
		_, c, ok := h.subscribe()
		if !ok {
			t.Fatal("capacity")
		}
		cancellations = append(cancellations, c)
	}
	if _, _, ok := h.subscribe(); ok {
		t.Fatal("unbounded screen count")
	}
	for _, c := range cancellations {
		c()
	}
	if _, c, ok := h.subscribe(); !ok {
		t.Fatal("capacity not released")
	} else {
		c()
	}
}

func TestStageSSESnapshotAndDisconnect(t *testing.T) {
	s := &Server{}
	local := httptest.NewServer(s.Handler())
	defer local.Close()
	resp, err := http.Get(local.URL + "/api/stage/events")
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(resp.Body)
	read := func() Presentation {
		t.Helper()
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		reader.ReadString('\n')
		var p Presentation
		if err = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if p := read(); p.Phase != "offline" {
		t.Fatal("no initial snapshot")
	}
	s.stage.reset("listening", true)
	if p := read(); p.Phase != "listening" {
		t.Fatal("no live state")
	}
	resp.Body.Close()
	deadline := time.Now().Add(time.Second)
	for {
		s.stage.mu.Lock()
		n := len(s.stage.subs)
		s.stage.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("subscriber leaked")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestStageViewGuardsAndAssets(t *testing.T) {
	s := &Server{DisplayAllowedHosts: []string{"display.test"}}
	for _, tc := range []struct {
		body, origin, device string
		code                 int
	}{
		{`{"url":"https://display.test/display/?task_ref=one"}`, "http://localhost:8090", "", 204},
		{`{"url":"https://display.test/display/?task_ref=two"}`, "", "1", 204},
		{`{"url":""}`, "http://localhost:8090", "", 204},
		{`{"url":"https://evil.test/"}`, "http://localhost:8090", "", 403},
		{`{"url":"javascript:alert(1)"}`, "http://localhost:8090", "", 403},
		{`{"url":"https://user:pass@display.test/"}`, "http://localhost:8090", "", 403},
		{`{"url":""}`, "https://evil.test", "1", 403},
		{`{"url":""}`, "", "", 403},
		{`{"url":"","unexpected":true}`, "http://localhost:8090", "", 400},
	} {
		r := httptest.NewRequest("POST", "http://localhost:8090/api/stage/view", strings.NewReader(tc.body))
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("X-Linxi-Device", tc.device)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != tc.code {
			t.Fatalf("%s: got %d want %d", tc.body, w.Code, tc.code)
		}
	}
	for _, path := range []string{"/stage/", "/stage/stage.js", "/stage/stage.css", "/playback.mjs", "/api/stage"} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "http://localhost:8090"+path, nil))
		if w.Code != 200 {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	r := httptest.NewRequest("GET", "http://localhost:8090/api/stage/events", nil)
	r.Header.Set("Origin", "https://evil.test")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin SSE accepted")
	}
}

func TestWakeDisplayRelay(t *testing.T) {
	code := http.StatusNoContent
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("X-Linxi-Device") != "1" {
			t.Error("bad wake request")
		}
		w.WriteHeader(code)
	}))
	defer upstream.Close()
	s := &Server{DisplayWakeURL: upstream.URL}
	call := func(origin string) int {
		r := httptest.NewRequest("POST", "http://localhost:8090/api/stage/wake", nil)
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w.Code
	}
	if call("https://evil.test") != 403 {
		t.Fatal("wake guard")
	}
	if call("http://localhost:8090") != 204 {
		t.Fatal("wake failed")
	}
	code = 503
	if call("http://localhost:8090") != 502 {
		t.Fatal("failure hidden")
	}
	s.DisplayWakeURL = ""
	if call("http://localhost:8090") != 204 {
		t.Fatal("standalone voice broken")
	}
	s.DisplayWakeURL = ":"
	if call("http://localhost:8090") != 503 {
		t.Fatal("invalid wake URL hidden")
	}
}

func TestStageSubscriberLimitHTTP(t *testing.T) {
	s := &Server{}
	for i := 0; i < 8; i++ {
		_, cancel, _ := s.stage.subscribe()
		defer cancel()
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "http://localhost:8090/api/stage/events", nil))
	if w.Code != 429 {
		t.Fatal("screen limit")
	}
}
