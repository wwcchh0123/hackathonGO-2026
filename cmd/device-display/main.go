// Command device-display is the Linux device-side display bridge. It reads
// the current session from the control service, renders it locally, and lets
// an external AirPlay sender mirror that local display.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/wwcchh0123/hackathonGO-2026/internal/devicecast"
	"github.com/wwcchh0123/hackathonGO-2026/internal/devicedisplay"
)

type config struct {
	ControlURL, ControlToken   string
	ListenAddr, ViewURL        string
	Browser                    string
	BrowserStart               bool
	Width, Height              int
	AirPlayTarget              string
	AirPlayPort                int
	AirPlayPassword            string
	AirPlayName                string
	AirPlayCredentials         string
	AirPlayFPS, AirPlayBitrate int
	AirPlayHWAccel             string
	AirPlayVideoCodec          string
	Doubletake                 string
}

func main() {
	if err := run(); err != nil {
		slog.Error("device display stopped", "error", err)
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

	store := devicedisplay.NewStore()
	view := devicedisplay.NewView(store, cfg.AirPlayName)
	listener, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen display: %w", err)
	}
	httpServer := &http.Server{Handler: view.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("display server stopped", "error", err)
			cancel()
		}
	}()

	client, err := devicedisplay.NewClient(devicedisplay.ClientConfig{ControlURL: cfg.ControlURL, Token: cfg.ControlToken}, store)
	if err != nil {
		return err
	}
	go func() {
		if err := client.Run(ctx); err != nil {
			slog.Error("control client stopped", "error", err)
			cancel()
		}
	}()

	if cfg.BrowserStart {
		go launchBrowser(ctx, cfg)
	}
	go func() {
		err := devicecast.New(devicecast.Config{
			Binary: cfg.Doubletake, Target: cfg.AirPlayTarget, Port: cfg.AirPlayPort,
			Password: cfg.AirPlayPassword, Credentials: cfg.AirPlayCredentials,
			FPS: cfg.AirPlayFPS, Bitrate: cfg.AirPlayBitrate,
			HWAccel: cfg.AirPlayHWAccel, VideoCodec: cfg.AirPlayVideoCodec,
		}).Run(ctx)
		if err != nil && ctx.Err() == nil {
			slog.Error("AirPlay sender stopped", "error", err)
			cancel()
		}
	}()

	slog.Info("device display ready", "control", cfg.ControlURL, "view", cfg.ViewURL, "airplay_target", cfg.AirPlayTarget)
	<-ctx.Done()
	shutdown, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	return httpServer.Shutdown(shutdown)
}

func loadConfig() (config, error) {
	// Keep device-only settings here instead of adding them to the control
	// service. The defaults describe the Raspberry Pi proof-of-concept and its
	// validated Windows receiver, so a local run works without a long command
	// line. Deployments can override every value through the environment.
	c := config{
		ControlURL:    env("CONTROL_URL", "http://127.0.0.1:8080"),
		ControlToken:  os.Getenv("CONTROL_TOKEN"),
		ListenAddr:    env("DISPLAY_LISTEN_ADDR", "127.0.0.1:8787"),
		ViewURL:       env("DISPLAY_URL", "http://127.0.0.1:8787"),
		Browser:       os.Getenv("BROWSER_BIN"),
		BrowserStart:  boolEnv("BROWSER_START", true),
		Width:         intEnv("SCREEN_WIDTH", 1920),
		Height:        intEnv("SCREEN_HEIGHT", 1080),
		AirPlayTarget: env("AIRPLAY_TARGET", "192.168.1.4"),
		AirPlayPort:   intEnv("AIRPLAY_PORT", 7001),
		// 3939 is the PIN of the local validation receiver. Set a different
		// value in any deployment that uses another screen or PIN.
		AirPlayPassword:    env("AIRPLAY_PASSWORD", "3939"),
		AirPlayName:        env("AIRPLAY_NAME", "Windows AirPlay@MISAKI-DESKTOP"),
		AirPlayCredentials: env("AIRPLAY_CREDENTIALS", "/tmp/doubletake/credentials.json"),
		AirPlayFPS:         intEnv("AIRPLAY_FPS", 20),
		AirPlayBitrate:     intEnv("AIRPLAY_BITRATE", 3000),
		AirPlayHWAccel:     env("AIRPLAY_HWACCEL", "auto"),
		AirPlayVideoCodec:  env("AIRPLAY_VIDEO_CODEC", "h264"),
		Doubletake:         env("DOUBLETAKE_BIN", "doubletake"),
	}
	if c.Width < 320 || c.Height < 240 {
		return c, fmt.Errorf("SCREEN_WIDTH and SCREEN_HEIGHT are too small")
	}
	if _, err := url.ParseRequestURI(c.ViewURL); err != nil {
		return c, fmt.Errorf("invalid DISPLAY_URL: %w", err)
	}
	return c, nil
}

func launchBrowser(ctx context.Context, cfg config) {
	binary := cfg.Browser
	if binary == "" {
		for _, candidate := range []string{"chromium", "chromium-browser", "google-chrome"} {
			if path, err := exec.LookPath(candidate); err == nil {
				binary = path
				break
			}
		}
	}
	if binary == "" {
		slog.Warn("browser not found; display server remains available", "set", "BROWSER_BIN")
		return
	}
	args := []string{
		"--no-sandbox", "--disable-dev-shm-usage", "--disable-gpu", "--hide-scrollbars",
		"--no-first-run", "--no-default-browser-check", "--disable-features=Translate,TranslateUI,MediaRouter",
		"--disable-translate", "--lang=zh-CN", "--window-position=0,0",
		fmt.Sprintf("--window-size=%d,%d", cfg.Width, cfg.Height), "--kiosk", "--app=" + withAirPlay(cfg.ViewURL, cfg.AirPlayName),
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil && ctx.Err() == nil {
		slog.Warn("browser exited", "error", err)
	}
}

func withAirPlay(raw, name string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	query := parsed.Query()
	query.Set("airplay", name)
	parsed.RawQuery = query.Encode()
	return parsed.String()
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

func boolEnv(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}
