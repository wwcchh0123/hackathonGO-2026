// Package voice serves a loopback-only microphone UI and bridges it to Lingxi.
package voice

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/wwcchh0123/hackathonGO-2026/internal/linx"
	"github.com/wwcchh0123/hackathonGO-2026/internal/voicecodec"
)

//go:embed ui/*
var assets embed.FS

var errSessionEnded = errors.New("lingxi session ended")

type Status struct {
	DeviceID       string `json:"device_id"`
	Stage          string `json:"stage"`
	Code           string `json:"code,omitempty"`
	Error          string `json:"error,omitempty"`
	AudioAvailable bool   `json:"audio_available"`
}

type Server struct {
	Gateway        linx.Gateway
	Dialer         linx.Dialer
	Codec          voicecodec.Factory
	AudioAvailable bool
	Logf           func(string, ...any)
	mu             sync.Mutex
	status         Status
	busy           atomic.Bool
}

func (s *Server) setStatus(stage, code, message string) {
	s.mu.Lock()
	s.status = Status{s.Gateway.Identity.DeviceID, stage, code, message, s.AudioAvailable}
	s.mu.Unlock()
}

// Pair prepares the device before the user opens a microphone session.
func (s *Server) Pair(ctx context.Context) error {
	s.setStatus("connecting", "", "")
	_, err := s.Gateway.Pair(ctx, func(a linx.Activation) {
		s.setStatus("activation", a.Code, "")
		if s.Logf != nil {
			s.Logf("灵矽设备 %s，激活码：%s；请在灵矽控制台添加设备并绑定智能体", s.Gateway.Identity.DeviceID, a.Code)
		}
	})
	if err != nil {
		s.setStatus("error", "", err.Error())
		return err
	}
	s.setStatus("paired", "", "")
	if s.Logf != nil {
		s.Logf("设备已绑定，可以在本地页面连接语音")
	}
	return nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		status := s.status
		s.mu.Unlock()
		status.DeviceID = s.Gateway.Identity.DeviceID
		status.AudioAvailable = s.AudioAvailable
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(status)
	})
	mux.HandleFunc("GET /api/voice", s.handleVoice)
	sub, _ := fs.Sub(assets, "ui")
	mux.Handle("GET /", http.FileServer(http.FS(sub)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			http.Error(w, "loopback host required", http.StatusForbidden)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; script-src 'self'; style-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		w.Header().Set("Permissions-Policy", "microphone=(self), camera=()")
		mux.ServeHTTP(w, r)
	})
}

type peer struct {
	ws *websocket.Conn
	mu sync.Mutex
}

func (p *peer) write(ctx context.Context, typ websocket.MessageType, data []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return p.ws.Write(wctx, typ, data)
}
func (p *peer) event(ctx context.Context, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return p.write(ctx, websocket.MessageText, data)
}

func (s *Server) handleVoice(w http.ResponseWriter, r *http.Request) {
	// Reject cross-site and non-browser callers before reserving the microphone.
	if r.Header.Get("Origin") != "http://"+r.Host {
		http.Error(w, "same-origin browser required", http.StatusForbidden)
		return
	}
	if !s.AudioAvailable {
		http.Error(w, "build with make voice-build", http.StatusServiceUnavailable)
		return
	}
	if !s.busy.CompareAndSwap(false, true) {
		http.Error(w, "another voice window is connected", http.StatusConflict)
		return
	}
	defer s.busy.Store(false)
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer ws.CloseNow()
	ws.SetReadLimit(32768)
	p := &peer{ws: ws}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	// Read immediately, including while waiting for activation, so a closed tab
	// cancels OTA polling. A bounded queue also prevents microphone backlog.
	in := make(chan packet, 8)
	readErr := make(chan error, 1)
	go func() {
		for {
			typ, data, err := ws.Read(ctx)
			if err != nil {
				readErr <- err
				cancel()
				return
			}
			select {
			case in <- packet{typ, data}:
			case <-ctx.Done():
				readErr <- ctx.Err()
				return
			default:
				readErr <- errors.New("microphone send queue overflow")
				cancel()
				return
			}
		}
	}()
	err = s.bridge(ctx, p, in)
	if err != nil && ctx.Err() == nil {
		if errors.Is(err, errSessionEnded) {
			_ = p.event(ctx, map[string]any{"type": "ended", "text": "本次语音会话已结束，点击重新连接即可继续。"})
		} else {
			_ = p.event(ctx, map[string]any{"type": "error", "text": err.Error()})
		}
	}
	cancel()
	ws.CloseNow()
	<-readErr
}

type packet struct {
	typ  websocket.MessageType
	data []byte
}

func (s *Server) bridge(ctx context.Context, p *peer, in <-chan packet) error {
	if err := p.event(ctx, map[string]any{"type": "status", "state": "connecting"}); err != nil {
		return err
	}
	creds, err := s.Gateway.Pair(ctx, func(a linx.Activation) {
		s.setStatus("activation", a.Code, "")
		_ = p.event(ctx, map[string]any{"type": "activation", "code": a.Code})
	})
	if err != nil {
		return err
	}
	s.setStatus("paired", "", "")
	cloud, err := s.Dialer.Connect(ctx, creds, s.Gateway.Identity)
	if err != nil {
		return err
	}
	defer cloud.Socket.CloseNow()
	codec := s.Codec
	if codec == nil {
		codec = voicecodec.Opus{}
	}
	decoder, err := codec.Decoder(cloud.Audio.SampleRate)
	if err != nil {
		return err
	}
	defer decoder.Close()
	if err = p.event(ctx, map[string]any{"type": "ready", "session_id": cloud.SessionID, "sample_rate": cloud.Audio.SampleRate, "input_sample_rate": 16000, "frame_samples": 960}); err != nil {
		return err
	}
	if s.Logf != nil {
		s.Logf("灵矽 hello 成功：输入 Opus 16000 Hz，输出 Opus %d Hz", cloud.Audio.SampleRate)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var recording, suppress atomic.Bool
	done := make(chan error, 2)
	go func() { done <- upload(ctx, p, cloud, codec, in, &recording, &suppress) }()
	go func() { done <- download(ctx, p, cloud, decoder, &recording, &suppress) }()
	err = <-done
	cancel()
	cloud.Socket.CloseNow()
	<-done // Codecs are not freed while a worker is still using them.
	return err
}

func upload(ctx context.Context, p *peer, cloud *linx.Connection, codec voicecodec.Factory, in <-chan packet, recording, suppress *atomic.Bool) error {
	var enc voicecodec.Encoder
	defer func() {
		if enc != nil {
			enc.Close()
		}
	}()
	tick := time.NewTicker(25 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := cloud.Socket.Ping(pctx)
			cancel()
			if err != nil {
				return errors.New("lingxi connection heartbeat failed; reconnect")
			}
		case frame := <-in:
			if frame.typ == websocket.MessageBinary {
				if !recording.Load() || enc == nil {
					return errors.New("audio received before start")
				}
				data, err := enc.Encode(frame.data)
				if err != nil {
					return err
				}
				if err = cloud.Write(ctx, websocket.MessageBinary, data); err != nil {
					return errors.New("could not send microphone audio")
				}
				continue
			}
			var command struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(frame.data, &command) != nil {
				return errors.New("invalid microphone command")
			}
			switch command.Type {
			case "start":
				if recording.Load() {
					return errors.New("recording already started")
				}
				if enc != nil {
					enc.Close()
					enc = nil
				}
				var err error
				enc, err = codec.Encoder()
				if err != nil {
					return err
				}
				recording.Store(true)
				suppress.Store(true)
				if err = cloud.Send(ctx, linx.Message{Type: "abort", Reason: "wake_word_detected"}); err != nil {
					return errors.New("could not interrupt speech")
				}
				if err = cloud.Send(ctx, linx.Message{Type: "listen", State: "start", Mode: "manual"}); err != nil {
					return errors.New("could not start listening")
				}
			case "stop":
				if !recording.Load() {
					continue
				}
				recording.Store(false)
				suppress.Store(false)
				if err := cloud.Send(ctx, linx.Message{Type: "listen", State: "stop", Mode: "manual"}); err != nil {
					return errors.New("could not stop listening")
				}
			case "abort":
				suppress.Store(true)
				if err := cloud.Send(ctx, linx.Message{Type: "abort", Reason: "wake_word_detected"}); err != nil {
					return errors.New("could not interrupt speech")
				}
			default:
				return fmt.Errorf("unsupported microphone command")
			}
		}
	}
}

func download(ctx context.Context, p *peer, cloud *linx.Connection, decoder voicecodec.Decoder, recording, suppress *atomic.Bool) error {
	for {
		typ, data, err := cloud.Socket.Read(ctx)
		if err != nil {
			return errors.New("灵矽连接已断开，请点击重新连接")
		}
		if typ == websocket.MessageBinary {
			if recording.Load() || suppress.Load() {
				continue
			}
			pcm, err := decoder.Decode(data)
			if err != nil {
				return errors.New("could not decode Lingxi Opus audio")
			}
			if err = p.write(ctx, websocket.MessageBinary, pcm); err != nil {
				return err
			}
			continue
		}
		var msg linx.Message
		if json.Unmarshal(data, &msg) != nil {
			return errors.New("invalid Lingxi event")
		}
		switch msg.Type {
		case "tts":
			if recording.Load() || suppress.Load() {
				continue
			}
			if msg.State == "start" {
				if err = decoder.Reset(); err != nil {
					return err
				}
			}
			if msg.IsAborted {
				suppress.Store(true)
			}
			if err = p.event(ctx, msg); err != nil {
				return err
			}
		case "stt", "notify":
			if err = p.event(ctx, msg); err != nil {
				return err
			}
		case "goodbye":
			return errSessionEnded
		case "error":
			return errors.New("灵矽报告会话错误，请检查设备绑定和智能体配置后重新连接")
		}
	}
}

// ValidateListen keeps the microphone service off public and LAN interfaces.
func ValidateListen(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return errors.New("voice client must listen on localhost or a loopback IP")
	}
	return nil
}
