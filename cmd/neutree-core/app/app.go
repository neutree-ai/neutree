package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"k8s.io/klog/v2"

	"github.com/neutree-ai/neutree/cmd/neutree-core/app/config"
	"github.com/neutree-ai/neutree/controllers"
	"github.com/neutree-ai/neutree/internal/cron"
	"github.com/neutree-ai/neutree/pkg/releaseprofile"
)

const (
	// serverShutdownTimeout bounds how long Run waits for in-flight requests
	// to drain after context cancellation before returning; the OS reclaims
	// the listener once the server is shut down.
	serverShutdownTimeout = 5 * time.Second

	// readHeaderTimeout bounds how long the server waits for a request header
	// before dropping the connection, guarding against slowloris-style
	// clients. Kept independent of the shutdown budget so that raising one
	// never silently weakens the other.
	readHeaderTimeout = 5 * time.Second
)

// App represents the main application
type App struct {
	config      *config.CoreConfig
	controllers map[string]controllers.Controller
}

// NewApp creates a new application instance
func NewApp(c *config.CoreConfig, controllers map[string]controllers.Controller) *App {
	return &App{
		config:      c,
		controllers: controllers,
	}
}

// Run starts the application
func (a *App) Run(ctx context.Context) error {
	klog.Infof("Starting Neutree Core Application")

	if err := releaseprofile.SynchronizeCurrentBaseline(a.config.Storage, a.config.Storage); err != nil {
		return fmt.Errorf("synchronize current release info: %w", err)
	}

	go a.config.ObsCollectConfigManager.Start(ctx)

	go cron.StartCrons(ctx, a.config.Storage) //nolint:errcheck

	// Start all controllers
	for name, ctrl := range a.controllers {
		go func(name string, ctrl controllers.Controller) {
			klog.Infof("Starting controller: %s", name)
			ctrl.Start(ctx)
		}(name, ctrl)
	}

	// Start core server
	coreServerListenAddr := fmt.Sprintf("%s:%d",
		a.config.ServerConfig.Host,
		a.config.ServerConfig.Port)
	klog.Infof("Starting core server on %s", coreServerListenAddr)

	server := &http.Server{
		Addr:              coreServerListenAddr,
		Handler:           a.config.GinEngine,
		ReadHeaderTimeout: readHeaderTimeout,
	}

	errCh := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}

		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), serverShutdownTimeout)
		defer cancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown core server: %w", err)
		}

		return <-errCh
	case err := <-errCh:
		return err
	}
}
