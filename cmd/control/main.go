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
	"github.com/wwcchh0123/hackathonGO-2026/internal/config"
	"github.com/wwcchh0123/hackathonGO-2026/internal/controlplane"
	"github.com/wwcchh0123/hackathonGO-2026/internal/runtimeprofile"
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
	bindings := make([]session.ProfileBinding, 0, len(cfg.Profiles.Profiles))
	var provider agentapi.Provider
	for _, runtime := range cfg.Profiles.Profiles {
		var current agentapi.Provider
		if cfg.Mode == "mock" {
			current = agentapi.NewMockForProfile(ctx, runtime)
		} else {
			httpProvider := agentapi.NewHTTP(cfg.AgentURL, cfg.AgentKey, runtime.AgentID, runtime.EnvironmentID).WithProfile(runtime)
			validateCtx, validateCancel := context.WithTimeout(ctx, 15*time.Second)
			err = httpProvider.ValidateProfile(validateCtx, runtime)
			validateCancel()
			if err != nil {
				return fmt.Errorf("validate AgentAPI profile %s: %w", runtime.Key, err)
			}
			if runtime.DefaultModel.ID == "" {
				model := httpProvider.EffectiveModel()
				runtime.DefaultModel.ID, runtime.DefaultModel.Effort = model.ID, model.Effort
				runtime.AllowedModels = []runtimeprofile.Model{runtime.DefaultModel}
			}
			if runtime.DefaultModel.Effort == "" {
				actual := httpProvider.EffectiveModel()
				runtime.DefaultModel.Effort = actual.Effort
				for i := range runtime.AllowedModels {
					if runtime.AllowedModels[i].ID == actual.ID && runtime.AllowedModels[i].Effort == "" {
						runtime.AllowedModels[i].Effort = actual.Effort
					}
				}
			}
			current = httpProvider
		}
		bindings = append(bindings, session.ProfileBinding{Profile: runtime, Provider: current})
		if runtime.Key == cfg.DefaultRuntime {
			provider = current
		}
	}
	if provider == nil && len(bindings) > 0 {
		provider = bindings[0].Provider
	}
	manager := controlplane.New(ctx, provider, cfg.DeviceID, cfg.DataDir, cfg.PublicURL, controlplane.Options{Session: session.Options{Profiles: bindings, DefaultProfile: cfg.DefaultRuntime}})
	srv := &http.Server{Addr: cfg.Addr, Handler: httpapi.HandlerWithManager(cfg, manager), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
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
	manager.Wait()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return shutdownErr
}
