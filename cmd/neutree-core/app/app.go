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
	"github.com/neutree-ai/neutree/internal/leader"
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

	// leaseReleaseTimeout bounds how long Run waits for the elector to give
	// up the lease on shutdown so a standby can take over without waiting
	// for the lease to expire.
	leaseReleaseTimeout = 5 * time.Second
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

// Run serves the core HTTP API on every replica and runs the background work
// (gateway init, controllers, crons, collect config sync) only on the leader.
func (a *App) Run(ctx context.Context) error {
	klog.Infof("Starting Neutree Core Application")

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	coreServerListenAddr := fmt.Sprintf("%s:%d",
		a.config.ServerConfig.Host,
		a.config.ServerConfig.Port)
	klog.Infof("Starting core server on %s", coreServerListenAddr)

	server := &http.Server{
		Addr:              coreServerListenAddr,
		Handler:           a.config.GinEngine,
		ReadHeaderTimeout: readHeaderTimeout,
	}

	serverErr := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
			return
		}

		serverErr <- nil
	}()

	leaderErr := make(chan error, 1)
	electorDone := make(chan struct{})

	go func() {
		defer close(electorDone)

		err := a.elector().Run(ctx, func(leaderCtx context.Context) {
			a.startLeaderWork(leaderCtx)
		})
		if err != nil {
			trySend(leaderErr, fmt.Errorf("leader election: %w", err))
		}
	}()

	var (
		runErr        error
		serverStopped bool
	)

	select {
	case <-ctx.Done():
	case runErr = <-serverErr:
		serverStopped = true
	case runErr = <-leaderErr:
	}

	// Cancelling lets the elector release the lease while the server drains.
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), serverShutdownTimeout)
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil && runErr == nil {
		runErr = fmt.Errorf("shutdown core server: %w", err)
	} else if !serverStopped {
		if err := <-serverErr; err != nil && runErr == nil {
			runErr = err
		}
	}

	select {
	case <-electorDone:
	case <-time.After(leaseReleaseTimeout):
		klog.Warningf("Leader election did not stop within %v", leaseReleaseTimeout)
	}

	return runErr
}

func (a *App) elector() leader.Elector {
	if a.config.LeaderElector == nil {
		return leader.AlwaysLeader{}
	}

	return a.config.LeaderElector
}

var (
	gatewayInitBackoffMin = 2 * time.Second
	gatewayInitBackoffMax = 30 * time.Second
)

// startLeaderWork starts everything that must run on exactly one replica.
func (a *App) startLeaderWork(ctx context.Context) {
	klog.Infof("Starting leader-only background work")

	if a.config.Gateway != nil {
		// A new leader often takes over while the gateway is also recovering;
		// exiting here would hand the lease around without anyone making progress.
		go a.initGatewayWithRetry(ctx)
	}

	go a.config.ObsCollectConfigManager.Start(ctx)

	go cron.StartCrons(ctx, a.config.Storage) //nolint:errcheck

	for name, ctrl := range a.controllers {
		go func(name string, ctrl controllers.Controller) {
			klog.Infof("Starting controller: %s", name)
			ctrl.Start(ctx)
		}(name, ctrl)
	}
}

func trySend(ch chan<- error, err error) {
	select {
	case ch <- err:
	default:
	}
}

// initGatewayWithRetry keeps calling Gateway.Init until it succeeds or the
// leader context ends. Init is idempotent (get-or-create).
func (a *App) initGatewayWithRetry(ctx context.Context) {
	backoff := gatewayInitBackoffMin

	for {
		err := a.config.Gateway.Init()
		if err == nil {
			klog.Infof("Gateway initialized")
			return
		}

		klog.Warningf("Init gateway failed, retrying in %v: %v", backoff, err)

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}

		backoff = min(backoff*2, gatewayInitBackoffMax)
	}
}
