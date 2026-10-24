// Package devicecast starts the external AirPlay screen-mirroring sender used
// by the Linux device image.
package devicecast

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

type Config struct {
	Binary      string
	Target      string
	Port        int
	Password    string
	Credentials string
	FPS         int
	Bitrate     int
	HWAccel     string
	VideoCodec  string
}

type Runner struct {
	cfg Config
}

func New(cfg Config) *Runner { return &Runner{cfg: cfg} }

// Run keeps the sender available while the device process is running. The
// sender is intentionally an external dependency: AirPlay screen mirroring
// needs access to the device display and its platform codecs. A sender exit
// is treated as recoverable, because a receiver can temporarily disappear
// from the LAN; Run retries after a short delay until the device shuts down.
func (r *Runner) Run(ctx context.Context) error {
	if r.cfg.Target == "" {
		slog.Info("AirPlay disabled", "reason", "AIRPLAY_TARGET is empty")
		return nil
	}
	if r.cfg.Binary == "" {
		return fmt.Errorf("AirPlay target is configured but DOUBLETAKE_BIN is empty")
	}

	for {
		err := r.runOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			slog.Warn("AirPlay sender exited", "error", err)
		} else {
			slog.Warn("AirPlay sender exited")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(4 * time.Second):
		}
	}
}

func (r *Runner) runOnce(ctx context.Context) error {
	if r.cfg.Credentials != "" {
		if err := os.MkdirAll(filepath.Dir(r.cfg.Credentials), 0o700); err != nil {
			return fmt.Errorf("prepare AirPlay credentials: %w", err)
		}
	}
	// These flags mirror the tested Linux arm64 setup: video-only mirroring,
	// H.264 encoding, and a low target latency suitable for a live dashboard.
	args := []string{
		"-target", r.cfg.Target,
		"-port", strconv.Itoa(r.cfg.Port),
		"-no-audio",
		"-fps", strconv.Itoa(r.cfg.FPS),
		"-bitrate", strconv.Itoa(r.cfg.Bitrate),
		"-hwaccel", r.cfg.HWAccel,
		"-video-codec", r.cfg.VideoCodec,
		"-target-latency-ms", "100",
	}
	if r.cfg.Credentials != "" {
		args = append(args, "-creds", r.cfg.Credentials)
	}

	cmd := exec.CommandContext(ctx, r.cfg.Binary, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "DOUBLETAKE_CODE="+r.cfg.Password)
	slog.Info("starting AirPlay sender", "target", r.cfg.Target, "port", r.cfg.Port)
	return cmd.Run()
}
