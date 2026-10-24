package linx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const DefaultOTA = "https://xrobo.qiniuapi.com/v1/ota/"
const UserAgent = "linx-mac-voice/0.1.0"

type Activation struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Credentials struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

type OTAResponse struct {
	Activation *Activation `json:"activation"`
	WebSocket  Credentials `json:"websocket"`
	Error      string      `json:"error"`
}

type Gateway struct {
	URL          string
	Identity     Identity
	BinaryHash   string
	HTTP         *http.Client
	PollInterval time.Duration
}

func (g Gateway) request(ctx context.Context, activate bool) ([]byte, int, error) {
	base := g.URL
	if base == "" {
		base = DefaultOTA
	}
	endpoint := strings.TrimRight(base, "/") + "/"
	var body io.Reader
	if activate {
		endpoint += "activate"
	} else {
		data, _ := json.Marshal(map[string]any{
			"version": 0, "uuid": g.Identity.ClientID, "mac_address": g.Identity.DeviceID,
			"application": map[string]any{"name": "linx-mac-voice", "version": "0.1.0", "elf_sha256": g.BinaryHash},
			"board":       map[string]any{"type": "linx-mac-voice", "ssid": "", "rssi": 0, "mac": g.Identity.DeviceID},
		})
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return nil, 0, errors.New("invalid OTA URL")
	}
	req.Header.Set("Device-Id", g.Identity.DeviceID)
	req.Header.Set("Client-Id", g.Identity.ClientID)
	req.Header.Set("Activation-Version", "1")
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept-Language", "zh-CN")
	req.Header.Set("Content-Type", "application/json")
	client := g.HTTP
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		return nil, 0, errors.New("OTA request failed; check network and gateway URL")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil || len(data) > 65536 {
		return nil, 0, errors.New("invalid OTA response size")
	}
	return data, resp.StatusCode, nil
}

func (g Gateway) Fetch(ctx context.Context) (OTAResponse, error) {
	var result OTAResponse
	data, status, err := g.request(ctx, false)
	if err != nil {
		return result, err
	}
	if status != http.StatusOK {
		return result, fmt.Errorf("OTA returned HTTP %d", status)
	}
	if json.Unmarshal(data, &result) != nil {
		return result, errors.New("OTA returned invalid JSON")
	}
	if result.Error != "" {
		return OTAResponse{}, errors.New("OTA rejected device metadata")
	}
	if result.WebSocket.Token == "" && (result.Activation == nil || result.Activation.Code == "") {
		return result, errors.New("OTA returned neither activation code nor device token")
	}
	return result, nil
}

func (g Gateway) Activated(ctx context.Context) (bool, error) {
	data, status, err := g.request(ctx, true)
	if err != nil {
		return false, err
	}
	if status == http.StatusAccepted {
		return false, nil
	}
	if status == http.StatusOK && strings.TrimSpace(string(data)) == "success" {
		return true, nil
	}
	return false, fmt.Errorf("activation check failed (HTTP %d)", status)
}

// Pair waits for the user to bind the displayed code. Tokens remain in memory.
func (g Gateway) Pair(ctx context.Context, show func(Activation)) (Credentials, error) {
	interval := g.PollInterval
	if interval == 0 {
		interval = 5 * time.Second
	}
	for {
		result, err := g.Fetch(ctx)
		if err != nil {
			return Credentials{}, err
		}
		if result.WebSocket.Token != "" {
			if result.WebSocket.URL == "" {
				return Credentials{}, errors.New("OTA omitted WebSocket URL")
			}
			return result.WebSocket, nil
		}
		if show != nil {
			show(*result.Activation)
		}
		refresh := time.NewTimer(time.Minute)
		tick := time.NewTicker(interval)
		wait := func() error {
			for {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-refresh.C:
					return nil
				case <-tick.C:
					bound, err := g.Activated(ctx)
					if err != nil {
						return err
					}
					if bound {
						return nil
					}
				}
			}
		}
		err = wait()
		refresh.Stop()
		tick.Stop()
		if err != nil {
			return Credentials{}, err
		}
	}
}
