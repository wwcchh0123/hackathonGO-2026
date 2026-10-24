package linx

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestHelloAndSessionControl(t *testing.T) {
	got := make(chan Message, 1)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("Protocol-Version") != "1" || r.Header.Get("Client-Id") != testIdentity.ClientID || r.Header.Get("Device-Id") != testIdentity.DeviceID {
			t.Error("incorrect WebSocket authentication")
		}
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer ws.CloseNow()
		_, b, err := ws.Read(r.Context())
		if err != nil {
			t.Error(err)
			return
		}
		var hello map[string]json.RawMessage
		if json.Unmarshal(b, &hello) != nil {
			t.Error("invalid hello")
		}
		var params AudioParams
		json.Unmarshal(hello["audio_params"], &params)
		if params.SampleRate != 16000 || params.FrameDuration != 60 || params.Format != "opus" {
			t.Error("incorrect input parameters")
		}
		var features map[string]any
		json.Unmarshal(hello["features"], &features)
		if features["mcp"] == true {
			t.Error("must not advertise unimplemented MCP")
		}
		ws.Write(r.Context(), websocket.MessageText, []byte(`{"type":"hello","transport":"websocket","session_id":"voice-session","audio_params":{"format":"opus","sample_rate":24000}}`))
		_, b, err = ws.Read(r.Context())
		if err != nil {
			t.Error(err)
			return
		}
		var msg Message
		json.Unmarshal(b, &msg)
		got <- msg
		ws.Read(r.Context())
	}))
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c, err := (Dialer{AllowInsecure: true}).Connect(ctx, Credentials{URL: "ws" + strings.TrimPrefix(s.URL, "http"), Token: "test-token"}, testIdentity)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Socket.CloseNow()
	if c.Audio.SampleRate != 24000 || c.Audio.Channels != 1 {
		t.Fatal("output parameters not negotiated")
	}
	if err := c.Send(ctx, Message{Type: "listen", State: "start", Mode: "manual", SessionID: "wrong-agentapi-id"}); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-got:
		if m.SessionID != "voice-session" || m.Mode != "manual" {
			t.Fatalf("incorrect control message: %+v", m)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestRejectInvalidHello(t *testing.T) {
	for _, response := range []string{`{"type":"error","token":"secret"}`, `{"type":"hello","transport":"websocket","audio_params":{"format":"pcm"}}`, `{"type":"hello","transport":"mqtt"}`, `{"type":"hello","transport":"websocket","audio_params":{"channels":2}}`, "timeout"} {
		t.Run(response, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ws, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer ws.CloseNow()
				ws.Read(r.Context())
				if response != "timeout" {
					ws.Write(r.Context(), websocket.MessageText, []byte(response))
				}
				ws.Read(r.Context())
			}))
			defer s.Close()
			_, err := (Dialer{AllowInsecure: true, HelloTimeout: 100 * time.Millisecond}).Connect(context.Background(), Credentials{URL: "ws" + strings.TrimPrefix(s.URL, "http"), Token: "secret"}, testIdentity)
			if err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatalf("invalid hello error: %v", err)
			}
		})
	}
}
