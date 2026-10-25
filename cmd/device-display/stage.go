package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"time"
)

func (r *runtime) stageEndpoint(path string) string {
	u, _ := url.Parse(r.cfg.StageURL)
	u.Path, u.RawQuery, u.Fragment = path, "", ""
	return u.String()
}

func (r *runtime) setStageView(ctx context.Context, rawURL string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	body, _ := json.Marshal(map[string]string{"url": rawURL})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.stageEndpoint("/api/stage/view"), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Linxi-Device", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("人物展示服务不可用: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("人物展示服务拒绝切换: HTTP %d", resp.StatusCode)
	}
	return nil
}

// Called with runtime.mu held, shared by MCP and local voice wake requests.
func (r *runtime) ensureStageLocked() error {
	if r.cfg.StageURL == "" || r.cfg.AirPlayTarget == "" {
		return fmt.Errorf("人物页面或 AirPlay 目标未配置")
	}
	if err := r.browser.ensureOpen(r.base, r.cfg.StageURL); err != nil {
		return err
	}
	r.startAirPlayLocked()
	return nil
}

func (b *browser) ensureOpen(ctx context.Context, rawURL string) error {
	b.mu.Lock()
	if b.cancel != nil && b.url == rawURL {
		select {
		case <-b.done:
			// Exit is broadcast by closing done; observing it never consumes it.
		default:
			b.mu.Unlock()
			return nil
		}
	}
	b.mu.Unlock()
	return b.Open(ctx, rawURL)
}

func (r *runtime) wakeHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /wake", func(w http.ResponseWriter, req *http.Request) {
		host, _, _ := net.SplitHostPort(req.RemoteAddr)
		if !net.ParseIP(host).IsLoopback() || req.Header.Get("Origin") != "" || req.Header.Get("X-Linxi-Device") != "1" {
			http.Error(w, "local device only", http.StatusForbidden)
			return
		}
		r.mu.Lock()
		err := r.ensureStageLocked()
		r.mu.Unlock()
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

func (r *runtime) startStage(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for attempt := 0; attempt < 30; attempt++ {
		probeCtx, cancel := context.WithTimeout(ctx, time.Second)
		req, _ := http.NewRequestWithContext(probeCtx, http.MethodGet, r.stageEndpoint("/api/stage"), nil)
		resp, err := http.DefaultClient.Do(req)
		ready := err == nil && resp.StatusCode == http.StatusOK
		if resp != nil {
			resp.Body.Close()
		}
		cancel()
		if ready {
			r.mu.Lock()
			err = r.ensureStageLocked()
			r.mu.Unlock()
			if err == nil {
				slog.Info("companion screen ready")
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
	slog.Warn("companion screen startup timed out; voice wake can retry")
}
