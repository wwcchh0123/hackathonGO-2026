package voice

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/wwcchh0123/hackathonGO-2026/internal/linx"
	"github.com/wwcchh0123/hackathonGO-2026/internal/voicecodec"
)

func socketPair(t *testing.T) (*websocket.Conn, *websocket.Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	accepted := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		accepted <- conn
		<-ctx.Done()
	}))
	client, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		cancel()
		server.Close()
		t.Fatal(err)
	}
	remote := <-accepted
	t.Cleanup(func() { cancel(); client.CloseNow(); remote.CloseNow(); server.Close() })
	return client, remote
}

func TestPairUpdatesPublicStatusAndReportsErrors(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/activate" {
			fmt.Fprint(w, "success")
			return
		}
		if calls.Add(1) == 1 {
			fmt.Fprint(w, `{"activation":{"code":"123456"}}`)
			return
		}
		fmt.Fprint(w, `{"websocket":{"url":"wss://example.test/ws","token":"private"}}`)
	}))
	defer upstream.Close()
	s := &Server{Gateway: linx.Gateway{URL: upstream.URL, PollInterval: time.Millisecond}, Logf: func(string, ...any) {}}
	if err := s.Pair(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.status.Stage != "paired" || s.status.Code != "" {
		t.Fatalf("status: %+v", s.status)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Pair(ctx); !errors.Is(err, context.Canceled) || s.status.Stage != "error" {
		t.Fatalf("pair error/status: %+v %v", s.status, err)
	}
	request := httptest.NewRequest("GET", "http://localhost/api/voice", nil)
	request.Header.Set("Origin", "http://localhost")
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, request)
	if response.Code != 503 {
		t.Fatalf("missing codec status=%d", response.Code)
	}
}

type failingFactory struct{}

func (failingFactory) Encoder() (voicecodec.Encoder, error) { return nil, errors.New("encoder failed") }
func (failingFactory) Decoder(int) (voicecodec.Decoder, error) {
	return nil, errors.New("decoder failed")
}

func TestUploadRejectsInvalidMicrophoneSequences(t *testing.T) {
	for _, test := range []struct {
		name   string
		frames []packet
		codec  voicecodec.Factory
		want   string
	}{
		{"audio before start", []packet{{websocket.MessageBinary, []byte{1}}}, testCodec{}, "audio received before start"},
		{"malformed command", []packet{{websocket.MessageText, []byte("{")}}, testCodec{}, "invalid microphone command"},
		{"unknown command", []packet{{websocket.MessageText, []byte(`{"type":"unknown"}`)}}, testCodec{}, "unsupported microphone command"},
		{"encoder failure", []packet{{websocket.MessageText, []byte(`{"type":"start"}`)}}, failingFactory{}, "encoder failed"},
		{"repeated start", []packet{{websocket.MessageText, []byte(`{"type":"start"}`)}, {websocket.MessageText, []byte(`{"type":"start"}`)}}, testCodec{}, "recording already started"},
		{"bad frame", []packet{{websocket.MessageText, []byte(`{"type":"start"}`)}, {websocket.MessageBinary, []byte{1}}}, testCodec{}, "bad microphone frame"},
		{"new round", []packet{{websocket.MessageText, []byte(`{"type":"stop"}`)}, {websocket.MessageText, []byte(`{"type":"start"}`)}, {websocket.MessageText, []byte(`{"type":"stop"}`)}, {websocket.MessageText, []byte(`{"type":"start"}`)}, {websocket.MessageText, []byte(`{"type":"unknown"}`)}}, testCodec{}, "unsupported microphone command"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, remote := socketPair(t)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			cloud := &linx.Connection{Socket: remote}
			// Drain control packets so the test checks protocol sequences, not network buffers.
			go func() {
				for {
					if _, _, err := client.Read(ctx); err != nil {
						return
					}
				}
			}()
			input := make(chan packet, len(test.frames))
			for _, frame := range test.frames {
				input <- frame
			}
			var recording, suppress atomic.Bool
			err := upload(ctx, &peer{stage: &stageHub{}}, cloud, test.codec, input, &recording, &suppress)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v want=%q", err, test.want)
			}
		})
	}
}

func TestDownloadRejectsBrokenEventsAndAudio(t *testing.T) {
	for _, test := range []struct {
		name string
		kind websocket.MessageType
		data string
		want string
	}{
		{"invalid event", websocket.MessageText, "{", "invalid Lingxi event"},
		{"cloud error", websocket.MessageText, `{"type":"error"}`, "灵矽报告会话错误"},
		{"invalid opus", websocket.MessageBinary, "broken", "could not decode"},
		{"goodbye", websocket.MessageText, `{"type":"goodbye"}`, "lingxi session ended"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, remote := socketPair(t)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := client.Write(ctx, test.kind, []byte(test.data)); err != nil {
				t.Fatal(err)
			}
			var recording, suppress atomic.Bool
			err := download(ctx, &peer{}, &linx.Connection{Socket: remote}, testDecoder{}, &recording, &suppress)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v want=%q", err, test.want)
			}
		})
	}
}
