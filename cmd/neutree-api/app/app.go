package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"k8s.io/klog/v2"

	"github.com/neutree-ai/neutree/cmd/neutree-api/app/config"
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

// App represents the main API application
type App struct {
	config *config.APIConfig
}

// NewApp creates a new API application instance
func NewApp(c *config.APIConfig) *App {
	return &App{
		config: c,
	}
}

// Run starts the API application
func (a *App) Run(ctx context.Context) error {
	klog.Infof("Starting Neutree API Application")

	// Start API server
	serverAddr := fmt.Sprintf("%s:%d", a.config.ServerConfig.Host, a.config.ServerConfig.Port)
	klog.Infof("Starting API server on %s", serverAddr)

	server := &http.Server{
		Addr:              serverAddr,
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
			return fmt.Errorf("shutdown API server: %w", err)
		}

		return <-errCh
	case err := <-errCh:
		return err
	}
}
