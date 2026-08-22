package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/cmd/neutree-core/app/config"
	"github.com/neutree-ai/neutree/controllers"
	"github.com/neutree-ai/neutree/internal/cron"
	"github.com/neutree-ai/neutree/pkg/releaseprofile"
	"github.com/neutree-ai/neutree/pkg/storage"
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

type currentBaselineSynchronizer func(
	releaseprofile.CurrentBaselineStore,
	string,
	releaseprofile.Builder,
) error

type baselineResolution struct {
	name              string
	shouldSynchronize bool
}

type currentBaselineStore struct {
	storage storage.Storage
}

func (store currentBaselineStore) ListReleaseInfo() ([]v1.ReleaseInfo, error) {
	return store.storage.ListReleaseInfo()
}

func (store currentBaselineStore) CreateReleaseInfo(info *v1.ReleaseInfo) error {
	return store.storage.CreateReleaseInfo(info)
}

func (store currentBaselineStore) UpdateReleaseInfo(id string, info *v1.ReleaseInfo) error {
	return store.storage.UpdateReleaseInfo(id, info)
}

func (store currentBaselineStore) ListClusterProfile() ([]v1.ClusterProfile, error) {
	return store.storage.ListClusterProfile(storage.ListOption{})
}

func (store currentBaselineStore) CreateClusterProfile(profile *v1.ClusterProfile) error {
	return store.storage.CreateClusterProfile(profile)
}

// App represents the main application
type App struct {
	config                     *config.CoreConfig
	controllers                map[string]controllers.Controller
	releaseProfileBuilder      releaseprofile.Builder
	synchronizeCurrentBaseline currentBaselineSynchronizer
}

// NewApp creates a new application instance
func NewApp(c *config.CoreConfig, controllers map[string]controllers.Controller) *App {
	return &App{
		config:                     c,
		controllers:                controllers,
		releaseProfileBuilder:      releaseprofile.NewBuilder(),
		synchronizeCurrentBaseline: releaseprofile.SynchronizeCurrentBaseline,
	}
}

// Run starts the application
func (a *App) Run(ctx context.Context) error {
	klog.Infof("Starting Neutree Core Application")

	baseline, err := a.currentControlPlaneBaseline()
	if err != nil {
		return err
	}

	if baseline.shouldSynchronize {
		if err := a.synchronizeCurrentBaseline(
			currentBaselineStore{storage: a.config.Storage},
			baseline.name,
			a.releaseProfileBuilder,
		); err != nil {
			return fmt.Errorf("synchronize current release info: %w", err)
		}
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

func (a *App) currentControlPlaneBaseline() (baselineResolution, error) {
	infos, err := a.config.Storage.ListReleaseInfo()
	if err != nil {
		return baselineResolution{}, fmt.Errorf("list release infos: %w", err)
	}

	baseline, err := releaseprofile.ResolveCurrentControlPlaneBaseline(a.config.Version, infos)
	if err == nil {
		// A development, dirty, or workflow-short-commit binary consumes the
		// persisted baseline selected above. It must not overwrite it using an
		// older local catalog that may not support that baseline.
		return baselineResolution{
			name:              baseline,
			shouldSynchronize: !releaseprofile.IsDevelopmentOrDirtyBuild(a.config.Version),
		}, nil
	}

	if !releaseprofile.IsDevelopmentOrDirtyBuild(a.config.Version) {
		return baselineResolution{}, fmt.Errorf("resolve current control-plane baseline: %w", err)
	}

	if a.releaseProfileBuilder == nil {
		return baselineResolution{}, fmt.Errorf("resolve current control-plane baseline: %w", err)
	}

	baseline = a.releaseProfileBuilder.CurrentReleaseInfoBaseline()

	normalizedBaseline, normalizeErr := releaseprofile.NormalizeControlPlaneRelease(baseline)
	if normalizeErr != nil {
		return baselineResolution{}, fmt.Errorf("current release info builder baseline %q must be an exact stable release info baseline", baseline)
	}

	if normalizedBaseline != baseline {
		return baselineResolution{}, fmt.Errorf("current release info builder baseline %q must be an exact stable release info baseline", baseline)
	}

	return baselineResolution{name: baseline, shouldSynchronize: true}, nil
}
