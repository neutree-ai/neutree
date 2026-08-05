package app

import (
	"context"
	"fmt"

	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/cmd/neutree-core/app/config"
	"github.com/neutree-ai/neutree/controllers"
	"github.com/neutree-ai/neutree/internal/cluster/releaseinfo"
	"github.com/neutree-ai/neutree/internal/cron"
	"github.com/neutree-ai/neutree/pkg/releaseprofile"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type currentBaselineSynchronizer func(
	releaseinfo.CurrentBaselineStore,
	string,
	releaseprofile.ReleaseInfoBuilder,
	releaseprofile.CurrentClusterProfileBuilder,
) error

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

func (store currentBaselineStore) UpdateClusterProfile(id string, profile *v1.ClusterProfile) error {
	return store.storage.UpdateClusterProfile(id, profile)
}

// App represents the main application
type App struct {
	config                       *config.CoreConfig
	controllers                  map[string]controllers.Controller
	releaseInfoBuilder           releaseprofile.ReleaseInfoBuilder
	currentClusterProfileBuilder releaseprofile.CurrentClusterProfileBuilder
	synchronizeCurrentBaseline   currentBaselineSynchronizer
}

// NewApp creates a new application instance
func NewApp(c *config.CoreConfig, controllers map[string]controllers.Controller) *App {
	return &App{
		config:                       c,
		controllers:                  controllers,
		releaseInfoBuilder:           releaseprofile.NewCommunityReleaseInfoBuilder(),
		currentClusterProfileBuilder: releaseprofile.NewCommunityClusterProfileBuilder(),
		synchronizeCurrentBaseline:   releaseinfo.SynchronizeCurrentBaseline,
	}
}

// Run starts the application
func (a *App) Run(ctx context.Context) error {
	klog.Infof("Starting Neutree Core Application")

	baseline, err := a.currentControlPlaneBaseline()
	if err != nil {
		return err
	}

	if err := a.synchronizeCurrentBaseline(
		currentBaselineStore{storage: a.config.Storage},
		baseline,
		a.releaseInfoBuilder,
		a.currentClusterProfileBuilder,
	); err != nil {
		return fmt.Errorf("synchronize current release info: %w", err)
	}

	// Start accelerator manager
	a.config.AcceleratorManager.Start(ctx)

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
	coreServerLinstenAddr := fmt.Sprintf("%s:%d",
		a.config.ServerConfig.Host,
		a.config.ServerConfig.Port)
	klog.Infof("Starting core server on %s", coreServerLinstenAddr)

	go func() {
		if err := a.config.GinEngine.Run(coreServerLinstenAddr); err != nil {
			klog.Fatalf("failed to start core server: %s", err.Error())
		}
	}()

	// Wait for context cancellation
	<-ctx.Done()

	return nil
}

func (a *App) currentControlPlaneBaseline() (string, error) {
	infos, err := a.config.Storage.ListReleaseInfo()
	if err != nil {
		return "", fmt.Errorf("list release infos: %w", err)
	}

	baseline, err := releaseinfo.ResolveCurrentControlPlaneBaseline(a.config.Version, infos)
	if err != nil {
		return "", fmt.Errorf("resolve current control-plane baseline: %w", err)
	}

	return baseline, nil
}
