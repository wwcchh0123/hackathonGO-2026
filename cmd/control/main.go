package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/wwcchh0123/hackathonGO-2026/internal/agentapi"
	"github.com/wwcchh0123/hackathonGO-2026/internal/artifacts"
	"github.com/wwcchh0123/hackathonGO-2026/internal/config"
	"github.com/wwcchh0123/hackathonGO-2026/internal/device"
	"github.com/wwcchh0123/hackathonGO-2026/internal/session"
	"github.com/wwcchh0123/hackathonGO-2026/internal/transport/httpapi"
)

func main() {
	if err := run(); err != nil {
		slog.Error("control service stopped", "error", err)
		os.Exit(1)
	}
}
func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	var provider agentapi.Provider = agentapi.NewMock(ctx)
	if cfg.Mode == "http" {
		httpProvider := agentapi.NewHTTP(cfg.AgentURL, cfg.AgentKey, cfg.AgentID, cfg.EnvironmentID).WithVersion(cfg.AgentVersion).WithVaults(cfg.VaultIDs)
		validateCtx, validateCancel := context.WithTimeout(ctx, 15*time.Second)
		if err := httpProvider.Validate(validateCtx); err != nil {
			validateCancel()
			return fmt.Errorf("validate AgentAPI configuration: %w", err)
		}
		validateCancel()
		provider = httpProvider
	}
	controller := session.New(ctx, provider, device.NewState(cfg.DeviceID), &artifacts.Store{Root: cfg.DataDir, PublicURL: cfg.PublicURL})
	srv := &http.Server{Addr: cfg.Addr, Handler: httpapi.Handler(cfg, controller), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	stopped := make(chan error, 1)
	go func() { stopped <- srv.ListenAndServe() }()
	slog.Info("control service listening", "addr", cfg.Addr, "mode", cfg.Mode, "device", cfg.DeviceID)
	select {
	case err = <-stopped:
		cancel()
	case <-ctx.Done():
	}
	shutdown, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()
	shutdownErr := srv.Shutdown(shutdown)
	controller.Wait()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return shutdownErr
}
