package linx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/coder/websocket"
)

type AudioParams struct {
	Format             string `json:"format"`
	SampleRate         int    `json:"sample_rate"`
	Channels           int    `json:"channels"`
	FrameDuration      int    `json:"frame_duration"`
	PlayBufferDuration int    `json:"play_buffer_duration,omitempty"`
}

type Message struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id,omitempty"`
	State     string `json:"state,omitempty"`
	Mode      string `json:"mode,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Text      string `json:"text,omitempty"`
	IsAborted bool   `json:"is_aborted,omitempty"`
	Event     string `json:"event,omitempty"`
}

type Connection struct {
	Socket    *websocket.Conn
	SessionID string
	Audio     AudioParams // Server output; microphone input is always 16 kHz / 60 ms.
}

type Dialer struct {
	AllowInsecure bool // For explicitly configured local test gateways only.
	HelloTimeout  time.Duration
}

func (d Dialer) Connect(ctx context.Context, creds Credentials, id Identity) (*Connection, error) {
	u, err := url.Parse(creds.URL)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "ws" && u.Scheme != "wss") {
		return nil, errors.New("invalid device WebSocket URL")
	}
	if u.Scheme == "ws" && !d.AllowInsecure {
		u.Scheme = "wss"
	}
	if creds.Token == "" {
		return nil, errors.New("device is not bound: OTA token is missing")
	}
	timeout := d.HelloTimeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	handshake, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	h := http.Header{}
	h.Set("Authorization", "Bearer "+creds.Token)
	h.Set("Device-Id", id.DeviceID)
	h.Set("Client-Id", id.ClientID)
	h.Set("Protocol-Version", "1")
	h.Set("User-Agent", UserAgent)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ws, resp, err := websocket.Dial(handshake, u.String(), &websocket.DialOptions{HTTPHeader: h, HTTPClient: client})
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("Lingxi WebSocket rejected connection (HTTP %d); reconnect to refresh the device token", resp.StatusCode)
		}
		return nil, errors.New("Lingxi WebSocket connection failed; check network and OTA configuration")
	}
	ws.SetReadLimit(1 << 20)
	ok := false
	defer func() {
		if !ok {
			ws.CloseNow()
		}
	}()
	input := AudioParams{Format: "opus", SampleRate: 16000, Channels: 1, FrameDuration: 60, PlayBufferDuration: 1000}
	hello, _ := json.Marshal(map[string]any{
		"type": "hello", "version": 1, "transport": "websocket", "audio_params": input,
		"features": map[string]any{"notify": map[string]bool{"config_updated": true}},
	})
	if err = ws.Write(handshake, websocket.MessageText, hello); err != nil {
		return nil, errors.New("could not send device hello")
	}
	typ, data, err := ws.Read(handshake)
	if err != nil {
		return nil, errors.New("Lingxi hello timed out or connection closed")
	}
	response := struct {
		Type      string      `json:"type"`
		Transport string      `json:"transport"`
		SessionID string      `json:"session_id"`
		Audio     AudioParams `json:"audio_params"`
	}{Audio: input}
	if typ != websocket.MessageText || json.Unmarshal(data, &response) != nil || response.Type != "hello" || response.Transport != "websocket" {
		return nil, errors.New("invalid Lingxi hello response; check device binding and agent configuration")
	}
	a := response.Audio
	validRate := a.SampleRate == 8000 || a.SampleRate == 12000 || a.SampleRate == 16000 || a.SampleRate == 24000 || a.SampleRate == 48000
	if a.Format != "opus" || a.Channels != 1 || !validRate {
		return nil, errors.New("unsupported Lingxi output audio parameters (expected mono Opus)")
	}
	ok = true
	return &Connection{ws, response.SessionID, a}, nil
}

func (c *Connection) Send(ctx context.Context, msg Message) error {
	msg.SessionID = c.SessionID
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return c.Write(ctx, websocket.MessageText, data)
}

func (c *Connection) Write(ctx context.Context, typ websocket.MessageType, data []byte) error {
	timeout, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return c.Socket.Write(timeout, typ, data)
}
