package devicedisplay

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/wwcchh0123/hackathonGO-2026/internal/device"
)

type ClientConfig struct {
	ControlURL string
	Token      string
	HTTPClient *http.Client
}

type Client struct {
	baseURL    *url.URL
	token      string
	httpClient *http.Client
	store      *Store
}

// NewClient validates the control-service origin once, then reuses it for the
// WebSocket snapshot channel and the per-session SSE channel. Keeping both
// URLs derived from one configured origin avoids accidentally connecting the
// device display to two different control services.
func NewClient(cfg ClientConfig, store *Store) (*Client, error) {
	base, err := url.Parse(strings.TrimRight(cfg.ControlURL, "/"))
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") {
		return nil, fmt.Errorf("invalid CONTROL_URL")
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{baseURL: base, token: cfg.Token, httpClient: httpClient, store: store}, nil
}

func (c *Client) Run(ctx context.Context) error {
	// The WebSocket is the device control channel. Reconnecting it is enough
	// to obtain a fresh snapshot; the currently selected session is then used
	// to rebuild the independent event stream below.
	backoff := time.Second
	for {
		err := c.runConnection(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			slog.Warn("control service connection lost", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if backoff < 10*time.Second {
			backoff *= 2
		}
	}
}

func (c *Client) runConnection(ctx context.Context) error {
	endpoint := *c.baseURL
	if endpoint.Scheme == "http" {
		endpoint.Scheme = "ws"
	} else {
		endpoint.Scheme = "wss"
	}
	endpoint.Path = "/api/device/channel"
	endpoint.RawQuery = ""

	header := make(http.Header)
	c.addAuth(header)
	conn, _, err := websocket.Dial(ctx, endpoint.String(), &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		return err
	}
	defer conn.Close(websocket.StatusNormalClosure, "device stopped")

	var cancelStream context.CancelFunc = func() {}
	defer func() { cancelStream() }()
	var session string
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		var snapshot device.Snapshot
		if err := json.Unmarshal(data, &snapshot); err != nil {
			return fmt.Errorf("decode device snapshot: %w", err)
		}
		if snapshot.SessionID != session {
			// Cancel the old SSE reader before changing the store. This ordering
			// prevents a late event from the previous session entering the new
			// timeline during a session switch.
			cancelStream()
			c.store.SetSnapshot(snapshot)
			streamCtx, cancel := context.WithCancel(ctx)
			cancelStream = cancel
			session = snapshot.SessionID
			if session != "" {
				go c.streamSession(streamCtx, session)
			}
			continue
		}
		c.store.SetSnapshot(snapshot)
	}
}

func (c *Client) streamSession(ctx context.Context, session string) {
	// Keep the SSE stream open for the whole session. The server sends history
	// first and then waits for live frames, so a normal idle status does not
	// close the connection.
	lastID := c.store.LastEventID(session)
	for {
		err := c.streamOnce(ctx, session, lastID)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			slog.Warn("session event stream lost", "session_id", session, "error", err)
		}
		lastID = c.store.LastEventID(session)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

func (c *Client) streamOnce(ctx context.Context, session, lastID string) error {
	// Last-Event-ID lets the server replay from the last frame the device
	// stored. AppendEvent remains the final guard because a server may replay
	// the cursor itself when a reconnect races with a live write.
	endpoint := *c.baseURL
	endpoint.Path = "/api/sessions/" + url.PathEscape(session) + "/events"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return err
	}
	c.addAuth(req.Header)
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("event stream returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return consumeSSE(resp.Body, func(frame sseFrame) error {
		if frame.Event != "agent_event" || frame.Data == "" {
			return nil
		}
		if !json.Valid([]byte(frame.Data)) {
			return fmt.Errorf("invalid agent event JSON")
		}
		c.store.AppendEvent(Event{ID: frame.ID, Data: json.RawMessage(frame.Data)})
		return nil
	})
}

func (c *Client) addAuth(header http.Header) {
	if c.token != "" {
		header.Set("Authorization", "Bearer "+c.token)
	}
}

type sseFrame struct {
	ID    string
	Event string
	Data  string
}

func consumeSSE(reader io.Reader, emit func(sseFrame) error) error {
	// Scanner's default 64 KiB limit is too small for a user prompt or tool
	// result. The larger bounded buffer preserves one complete SSE data line
	// without allowing an unbounded response to consume device memory.
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	var frame sseFrame
	var data strings.Builder
	flush := func() error {
		if data.Len() == 0 && frame.ID == "" && frame.Event == "" {
			return nil
		}
		frame.Data = data.String()
		if err := emit(frame); err != nil {
			return err
		}
		frame = sseFrame{}
		data.Reset()
		return nil
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		switch {
		case line == "":
			if err := flush(); err != nil {
				return err
			}
		case strings.HasPrefix(line, ":"):
			continue
		case strings.HasPrefix(line, "id:"):
			frame.ID = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
		case strings.HasPrefix(line, "event:"):
			frame.Event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			value := strings.TrimPrefix(line, "data:")
			if strings.HasPrefix(value, " ") {
				value = value[1:]
			}
			data.WriteString(value)
		}
	}
	if err := flush(); err != nil {
		return err
	}
	return scanner.Err()
}
