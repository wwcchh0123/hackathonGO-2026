package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/wwcchh0123/hackathonGO-2026/internal/linx"
	"github.com/wwcchh0123/hackathonGO-2026/internal/voicecodec"
)

type testCodec struct{}
type testEncoder struct{}
type testDecoder struct{}

func (testCodec) Encoder() (voicecodec.Encoder, error) { return testEncoder{}, nil }
func (testCodec) Decoder(rate int) (voicecodec.Decoder, error) {
	if rate != 24000 {
		return nil, errors.New("wrong negotiated output rate")
	}
	return testDecoder{}, nil
}
func (testEncoder) Encode(pcm []byte) ([]byte, error) {
	if len(pcm) != 1920 {
		return nil, errors.New("bad microphone frame")
	}
	return []byte("encoded-audio"), nil
}
func (testEncoder) Close() {}
func (testDecoder) Decode(packet []byte) ([]byte, error) {
	if string(packet) != "tts-opus" {
		return nil, errors.New("bad TTS packet")
	}
	return bytes.Repeat([]byte{0x34, 0x12}, 1440), nil
}
func (testDecoder) Reset() error { return nil }
func (testDecoder) Close()       {}

func TestBrowserToCloudVoiceRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cloudDone := make(chan struct{})
	var cloudURL string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ws" {
			fmt.Fprintf(w, `{"websocket":{"url":%q,"token":"cloud-secret"}}`, cloudURL)
			return
		}
		defer close(cloudDone)
		if r.Header.Get("Authorization") != "Bearer cloud-secret" {
			t.Error("cloud token missing")
		}
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer ws.CloseNow()
		read := func() (websocket.MessageType, []byte) {
			typ, b, err := ws.Read(ctx)
			if err != nil {
				t.Error(err)
			}
			return typ, b
		}
		read()
		ws.Write(ctx, websocket.MessageText, []byte(`{"type":"hello","transport":"websocket","session_id":"lingxi-session","audio_params":{"format":"opus","sample_rate":24000,"channels":1}}`))
		for _, want := range []string{"abort", "listen"} {
			_, b := read()
			var m linx.Message
			json.Unmarshal(b, &m)
			if m.Type != want || m.SessionID != "lingxi-session" {
				t.Errorf("unexpected control: %s", b)
			}
		}
		typ, b := read()
		if typ != websocket.MessageBinary || string(b) != "encoded-audio" {
			t.Error("microphone was not encoded")
		}
		_, b = read()
		var stop linx.Message
		json.Unmarshal(b, &stop)
		if stop.Type != "listen" || stop.State != "stop" {
			t.Error("missing stop")
		}
		for _, event := range []string{`{"type":"stt","text":"你好"}`, `{"type":"tts","state":"start"}`, `{"type":"tts","state":"sentence_start","text":"连接成功"}`} {
			ws.Write(ctx, websocket.MessageText, []byte(event))
		}
		ws.Write(ctx, websocket.MessageBinary, []byte("tts-opus"))
		ws.Write(ctx, websocket.MessageText, []byte(`{"type":"tts","state":"stop"}`))
		// Late audio after the local abort must be dropped even if the server
		// emits another TTS start before acknowledging the interruption.
		_, b = read()
		var abort linx.Message
		json.Unmarshal(b, &abort)
		if abort.Type != "abort" {
			t.Error("missing TTS abort")
		}
		ws.Write(ctx, websocket.MessageText, []byte(`{"type":"tts","state":"start"}`))
		ws.Write(ctx, websocket.MessageBinary, []byte("tts-opus"))
		ws.Write(ctx, websocket.MessageText, []byte(`{"type":"notify","event":"config_updated"}`))
		ws.Read(ctx)
	}))
	defer upstream.Close()
	cloudURL = "ws" + strings.TrimPrefix(upstream.URL, "http") + "/ws"
	s := &Server{Gateway: linx.Gateway{URL: upstream.URL}, Dialer: linx.Dialer{AllowInsecure: true}, Codec: testCodec{}, AudioAvailable: true}
	local := httptest.NewServer(s.Handler())
	defer local.Close()
	url := "ws" + strings.TrimPrefix(local.URL, "http") + "/api/voice"
	dial := func() (*websocket.Conn, *http.Response, error) {
		return websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": []string{local.URL}}})
	}
	browser, _, err := dial()
	if err != nil {
		t.Fatal(err)
	}
	defer browser.CloseNow()
	read := func() (websocket.MessageType, []byte) {
		typ, b, err := browser.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return typ, b
	}
	for {
		_, b := read()
		if bytes.Contains(b, []byte("cloud-secret")) {
			t.Fatal("device token leaked")
		}
		var m struct {
			Type string `json:"type"`
		}
		json.Unmarshal(b, &m)
		if m.Type == "ready" {
			break
		}
	}
	if other, resp, err := dial(); err == nil {
		other.CloseNow()
		t.Fatal("second voice tab accepted")
	} else if resp == nil || resp.StatusCode != 409 {
		t.Fatal("expected busy conflict")
	}
	for _, v := range []string{`{"type":"start"}`} {
		browser.Write(ctx, websocket.MessageText, []byte(v))
	}
	browser.Write(ctx, websocket.MessageBinary, make([]byte, 1920))
	browser.Write(ctx, websocket.MessageText, []byte(`{"type":"stop"}`))
	var stt, tts, audio bool
	var speechTurn uint64
	for {
		typ, b := read()
		if typ == websocket.MessageBinary {
			audio = bytes.Equal(b, bytes.Repeat([]byte{0x34, 0x12}, 1440))
			continue
		}
		var m linx.Message
		json.Unmarshal(b, &m)
		if m.Type == "tts" && m.State == "start" {
			var event struct {
				Turn uint64 `json:"turn_id"`
			}
			json.Unmarshal(b, &event)
			speechTurn = event.Turn
		}
		if m.Type == "stt" && m.Text == "你好" {
			stt = true
		}
		if m.State == "sentence_start" && m.Text == "连接成功" {
			tts = true
		}
		if m.Type == "tts" && m.State == "stop" {
			break
		}
	}
	if !stt || !tts || !audio {
		t.Fatal("incomplete voice round trip")
	}
	if speechTurn == 0 {
		t.Fatal("missing presentation generation")
	}
	browser.Write(ctx, websocket.MessageText, []byte(fmt.Sprintf(`{"type":"playback","turn_id":%d,"state":"playing","level":0.6,"text":"连接成功"}`, speechTurn)))
	deadline := time.Now().Add(time.Second)
	for s.stage.snapshot().Phase != "speaking" {
		if time.Now().After(deadline) {
			t.Fatal("playback feedback did not reach screen after cloud TTS stop")
		}
		time.Sleep(time.Millisecond)
	}
	if p := s.stage.snapshot(); p.UserText != "你好" || p.Text != "连接成功" {
		t.Fatalf("lost presentation text: %+v", p)
	}
	browser.Write(ctx, websocket.MessageText, []byte(`{"type":"abort"}`))
	typ, b := read()
	var notice linx.Message
	json.Unmarshal(b, &notice)
	if typ != websocket.MessageText || notice.Type != "notify" {
		t.Fatal("late audio was not suppressed")
	}
	browser.CloseNow()
	select {
	case <-cloudDone:
	case <-ctx.Done():
		t.Fatal("browser disconnect did not close cloud session")
	}
}

func TestLoopbackAndOriginGuards(t *testing.T) {
	s := &Server{AudioAvailable: true}
	h := s.Handler()
	for _, tc := range []struct {
		host, origin string
		code         int
	}{
		{"evil.example:8090", "http://evil.example:8090", 403},
		{"127.0.0.1:8090", "https://evil.example", 403},
		{"localhost:8090", "", 403},
	} {
		r := httptest.NewRequest("GET", "http://"+tc.host+"/api/voice", nil)
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.code {
			t.Fatalf("guard returned %d", w.Code)
		}
	}
	for _, address := range []string{"0.0.0.0:8090", "192.168.1.1:8090", ":8090"} {
		if ValidateListen(address) == nil {
			t.Fatalf("allowed public listener %s", address)
		}
	}
	for _, address := range []string{"127.0.0.1:8090", "localhost:8090", "[::1]:8090"} {
		if err := ValidateListen(address); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"/", "/app.js", "/mic-worklet.js", "/pcm.mjs", "/style.css", "/api/status"} {
		r := httptest.NewRequest("GET", "http://localhost:8090"+path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Errorf("%s returned %d", path, w.Code)
		}
		if strings.HasSuffix(path, ".mjs") && !strings.Contains(w.Header().Get("Content-Type"), "javascript") {
			t.Fatal("worklet module MIME type is not JavaScript")
		}
	}
}

func TestClosingTabCancelsActivation(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"activation":{"code":"123456"}}`) }))
	defer upstream.Close()
	s := &Server{Gateway: linx.Gateway{URL: upstream.URL}, AudioAvailable: true}
	local := httptest.NewServer(s.Handler())
	defer local.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(local.URL, "http")+"/api/voice", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": []string{local.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	for {
		_, b, err := ws.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(b, []byte(`"activation"`)) {
			break
		}
	}
	ws.CloseNow()
	for s.busy.Load() {
		select {
		case <-ctx.Done():
			t.Fatal("activation poll leaked after tab closed")
		case <-time.After(time.Millisecond):
		}
	}
	resp, err := http.Get(local.URL + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(b, []byte("123456")) {
		t.Fatal("activation code missing from UI status")
	}
}

func TestCloudGoodbyeIsNormalSessionEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var cloudURL string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ws" {
			fmt.Fprintf(w, `{"websocket":{"url":%q,"token":"test-token"}}`, cloudURL)
			return
		}
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		ws.Read(ctx)
		ws.Write(ctx, websocket.MessageText, []byte(`{"type":"hello","transport":"websocket","audio_params":{"sample_rate":24000}}`))
		ws.Write(ctx, websocket.MessageText, []byte(`{"type":"goodbye"}`))
		ws.Read(ctx)
	}))
	defer upstream.Close()
	cloudURL = "ws" + strings.TrimPrefix(upstream.URL, "http") + "/ws"
	s := &Server{Gateway: linx.Gateway{URL: upstream.URL}, Dialer: linx.Dialer{AllowInsecure: true}, Codec: testCodec{}, AudioAvailable: true}
	local := httptest.NewServer(s.Handler())
	defer local.Close()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(local.URL, "http")+"/api/voice", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": []string{local.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	for {
		_, b, err := ws.Read(ctx)
		if err != nil {
			t.Fatal("closed before session-end event:", err)
		}
		var event linx.Message
		if err := json.Unmarshal(b, &event); err != nil {
			t.Fatal(err)
		}
		if event.Type == "error" {
			t.Fatal("normal goodbye presented as an error")
		}
		if event.Type == "ended" {
			break
		}
	}
}
