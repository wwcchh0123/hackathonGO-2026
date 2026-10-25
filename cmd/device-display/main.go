// Command device-display simulates the Linux-side Linxi hardware.
//
// The simulated device exposes a device MCP endpoint. When the Linxi cloud
// calls cast_screen, it opens the supplied public session page in Chromium
// and mirrors that local browser display through AirPlay. Session events are
// therefore rendered by the control service page; this process does not call
// AgentAPI or hold a control-service token.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/wwcchh0123/hackathonGO-2026/internal/devicecast"
	"github.com/wwcchh0123/hackathonGO-2026/internal/devicecontrol"
)

type config struct {
	ListenAddr        string
	Browser           string
	BrowserUserAgent  string
	Width, Height     int
	AllowedHosts      []string
	AirPlayTarget     string
	AirPlayPort       int
	AirPlayPassword   string
	AirPlayName       string
	AirPlayCredential string
	AirPlayFPS        int
	AirPlayBitrate    int
	AirPlayHWAccel    string
	AirPlayVideoCode  string
	Doubletake        string
	BrowserDebugPort  int
}

type runtime struct {
	base context.Context
	cfg  config

	browser *browser
	cast    *devicecast.Runner

	mu            sync.Mutex
	airplayCancel context.CancelFunc
}

func main() {
	if err := run(); err != nil {
		slog.Error("device MCP stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	rt := &runtime{
		base:    ctx,
		cfg:     cfg,
		browser: &browser{binary: cfg.Browser, userAgent: cfg.BrowserUserAgent, width: cfg.Width, height: cfg.Height, debugPort: cfg.BrowserDebugPort},
		cast: devicecast.New(devicecast.Config{
			Binary: cfg.Doubletake, Target: cfg.AirPlayTarget, Port: cfg.AirPlayPort,
			Password: cfg.AirPlayPassword, Credentials: cfg.AirPlayCredential,
			FPS: cfg.AirPlayFPS, Bitrate: cfg.AirPlayBitrate,
			HWAccel: cfg.AirPlayHWAccel, VideoCodec: cfg.AirPlayVideoCode,
		}),
	}
	listener, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen device MCP: %w", err)
	}
	server := &http.Server{Handler: devicecontrol.Handler(rt), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("device MCP HTTP server stopped", "error", err)
			cancel()
		}
	}()

	slog.Info("device MCP ready", "listen", cfg.ListenAddr, "airplay_target", cfg.AirPlayTarget, "receiver", cfg.AirPlayName)
	<-ctx.Done()
	rt.StopCast(context.Background())
	shutdown, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	return server.Shutdown(shutdown)
}

func (r *runtime) ScanScreens(context.Context) ([]devicecontrol.Screen, error) {
	if r.cfg.AirPlayTarget == "" {
		return []devicecontrol.Screen{}, nil
	}
	return []devicecontrol.Screen{{
		ID:   fmt.Sprintf("airplay:%s:%d", r.cfg.AirPlayTarget, r.cfg.AirPlayPort),
		Name: r.cfg.AirPlayName,
	}}, nil
}

func (r *runtime) CastScreen(ctx context.Context, rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("网页地址无效")
	}
	if len(r.cfg.AllowedHosts) > 0 && !contains(r.cfg.AllowedHosts, parsed.Hostname()) {
		return fmt.Errorf("网页地址不在允许的展示服务范围内")
	}
	if r.cfg.AirPlayTarget == "" {
		return fmt.Errorf("没有配置 AirPlay 投屏目标")
	}
	if err := r.browser.Open(r.base, rawURL); err != nil {
		return err
	}
	r.mu.Lock()
	if r.airplayCancel == nil {
		airplayCtx, cancel := context.WithCancel(r.base)
		r.airplayCancel = cancel
		go func() {
			if err := r.cast.Run(airplayCtx); err != nil && ctx.Err() == nil {
				slog.Error("AirPlay sender stopped", "error", err)
			}
		}()
	}
	r.mu.Unlock()
	slog.Info("cast screen", "url", rawURL, "target", r.cfg.AirPlayName)
	return nil
}

func (r *runtime) ScrollScreen(ctx context.Context, direction string, pages int) error {
	return r.browser.Scroll(ctx, direction, pages)
}

func (r *runtime) StopCast(context.Context) error {
	r.mu.Lock()
	cancel := r.airplayCancel
	r.airplayCancel = nil
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return r.browser.Stop()
}

type browser struct {
	mu        sync.Mutex
	binary    string
	userAgent string
	width     int
	height    int
	debugPort int
	cancel    context.CancelFunc
	done      <-chan error
}

func (b *browser) Open(parent context.Context, rawURL string) error {
	if err := b.Stop(); err != nil {
		return err
	}
	binary := b.binary
	if binary == "" {
		for _, candidate := range []string{"chromium", "chromium-browser", "google-chrome"} {
			if path, err := exec.LookPath(candidate); err == nil {
				binary = path
				break
			}
		}
	}
	if binary == "" {
		return fmt.Errorf("找不到 Chromium，请设置 BROWSER_BIN")
	}
	ctx, cancel := context.WithCancel(parent)
	args := []string{
		"--no-sandbox", "--disable-dev-shm-usage", "--disable-gpu", "--hide-scrollbars",
		"--no-first-run", "--no-default-browser-check", "--disable-features=Translate,TranslateUI,MediaRouter",
		"--disable-translate", "--lang=zh-CN", "--window-position=0,0",
		"--user-agent=" + b.userAgent,
		"--remote-debugging-address=127.0.0.1", fmt.Sprintf("--remote-debugging-port=%d", b.debugPort),
		fmt.Sprintf("--window-size=%d,%d", b.width, b.height), "--kiosk", "--app=" + rawURL,
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("启动浏览器: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	b.mu.Lock()
	b.cancel = cancel
	b.done = done
	b.mu.Unlock()
	return nil
}

// Scroll sends a mouse-wheel event through Chromium's local DevTools
// endpoint. The device keeps the page in its own browser process, so the
// control MCP can change the visible event range without knowing anything
// about AgentAPI sessions or modifying their state.
func (b *browser) Scroll(ctx context.Context, direction string, pages int) error {
	if direction != "up" && direction != "down" {
		return fmt.Errorf("滚动方向无效")
	}
	if pages < 1 || pages > 5 {
		return fmt.Errorf("滚动页数无效，需要 1 到 5")
	}
	if b.debugPort <= 0 {
		return fmt.Errorf("浏览器调试端口未配置")
	}
	var target devtoolsTarget
	deadline := time.Now().Add(5 * time.Second)
	for {
		targets, err := b.targets(ctx)
		if err == nil {
			for _, candidate := range targets {
				if candidate.Type == "page" && candidate.WebSocketDebuggerURL != "" {
					target = candidate
					break
				}
			}
			if target.WebSocketDebuggerURL != "" {
				break
			}
		}
		if time.Now().After(deadline) {
			if err != nil {
				return fmt.Errorf("连接浏览器调试接口: %w", err)
			}
			return fmt.Errorf("没有找到投屏页面")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}

	conn, _, err := websocket.Dial(ctx, target.WebSocketDebuggerURL, nil)
	if err != nil {
		return fmt.Errorf("连接浏览器页面: %w", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "scroll complete")
	delta := float64(720 * pages)
	if direction == "up" {
		delta = -delta
	}
	message, err := json.Marshal(map[string]any{
		"id":     1,
		"method": "Input.dispatchMouseEvent",
		"params": map[string]any{
			"type":   "mouseWheel",
			"x":      float64(b.width / 2),
			"y":      float64(b.height / 2),
			"deltaY": delta,
			"deltaX": 0,
		},
	})
	if err != nil {
		return err
	}
	if err := conn.Write(ctx, websocket.MessageText, message); err != nil {
		return fmt.Errorf("发送滚动指令: %w", err)
	}
	readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	for {
		_, data, err := conn.Read(readCtx)
		if err != nil {
			return fmt.Errorf("等待浏览器滚动结果: %w", err)
		}
		var response struct {
			ID    int             `json:"id"`
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal(data, &response) != nil || response.ID != 1 {
			continue
		}
		if len(response.Error) > 0 && string(response.Error) != "null" {
			return fmt.Errorf("浏览器拒绝滚动: %s", response.Error)
		}
		return nil
	}
}

type devtoolsTarget struct {
	Type                 string `json:"type"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

func (b *browser) targets(ctx context.Context) ([]devtoolsTarget, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/json/list", b.debugPort), nil)
	if err != nil {
		return nil, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("浏览器调试接口返回 HTTP %d", response.StatusCode)
	}
	var targets []devtoolsTarget
	if err := json.NewDecoder(response.Body).Decode(&targets); err != nil {
		return nil, err
	}
	return targets, nil
}

func (b *browser) Stop() error {
	b.mu.Lock()
	cancel, done := b.cancel, b.done
	b.cancel, b.done = nil, nil
	b.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-time.After(2 * time.Second):
		return fmt.Errorf("浏览器退出超时")
	}
}

func loadConfig() (config, error) {
	c := config{
		ListenAddr:        env("DEVICE_MCP_LISTEN_ADDR", "127.0.0.1:8787"),
		Browser:           os.Getenv("BROWSER_BIN"),
		BrowserUserAgent:  env("BROWSER_USER_AGENT", "LinxiDevice/0.1"),
		Width:             intEnv("SCREEN_WIDTH", 1920),
		Height:            intEnv("SCREEN_HEIGHT", 1080),
		AllowedHosts:      splitEnv("DISPLAY_ALLOWED_HOSTS"),
		AirPlayTarget:     env("AIRPLAY_TARGET", "192.168.1.4"),
		AirPlayPort:       intEnv("AIRPLAY_PORT", 7001),
		AirPlayPassword:   env("AIRPLAY_PASSWORD", "3939"),
		AirPlayName:       env("AIRPLAY_NAME", "Windows AirPlay@MISAKI-DESKTOP"),
		AirPlayCredential: env("AIRPLAY_CREDENTIALS", "/tmp/doubletake/credentials.json"),
		AirPlayFPS:        intEnv("AIRPLAY_FPS", 20),
		AirPlayBitrate:    intEnv("AIRPLAY_BITRATE", 3000),
		AirPlayHWAccel:    env("AIRPLAY_HWACCEL", "auto"),
		AirPlayVideoCode:  env("AIRPLAY_VIDEO_CODEC", "h264"),
		Doubletake:        env("DOUBLETAKE_BIN", "doubletake"),
		BrowserDebugPort:  intEnv("BROWSER_DEBUG_PORT", 9222),
	}
	if c.Width < 320 || c.Height < 240 {
		return c, fmt.Errorf("SCREEN_WIDTH and SCREEN_HEIGHT are too small")
	}
	if c.AirPlayCredential != "" && filepath.Dir(c.AirPlayCredential) != "." {
		if err := os.MkdirAll(filepath.Dir(c.AirPlayCredential), 0o700); err != nil {
			return c, fmt.Errorf("prepare AirPlay credential directory: %w", err)
		}
	}
	return c, nil
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func splitEnv(key string) []string {
	var values []string
	for _, value := range strings.Split(os.Getenv(key), ",") {
		if value = strings.TrimSpace(value); value != "" {
			values = append(values, value)
		}
	}
	return values
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func intEnv(key string, fallback int) int {
	value, err := strconv.Atoi(env(key, strconv.Itoa(fallback)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}
