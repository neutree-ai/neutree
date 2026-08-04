package app

import (
	"context"
	"fmt"

	"k8s.io/klog/v2"

	"github.com/neutree-ai/neutree/cmd/neutree-api/app/config"
	"github.com/neutree-ai/neutree/internal/cluster/releaseinfo"
)

type releaseInfoSynchronizer func(releaseinfo.Store, string) (releaseinfo.SyncResult, error)

// App represents the main API application
type App struct {
	config                 *config.APIConfig
	synchronizeReleaseInfo releaseInfoSynchronizer
}

// NewApp creates a new API application instance
func NewApp(c *config.APIConfig) *App {
	return &App{
		config:                 c,
		synchronizeReleaseInfo: releaseinfo.SynchronizeSeed,
	}
}

// Run starts the API application
func (a *App) Run(ctx context.Context) error {
	klog.Infof("Starting Neutree API Application")

	if err := a.synchronizeCurrentReleaseInfo(); err != nil {
		return err
	}

	// Start API server
	serverAddr := fmt.Sprintf("%s:%d", a.config.ServerConfig.Host, a.config.ServerConfig.Port)
	klog.Infof("Starting API server on %s", serverAddr)

	go func() {
		if err := a.config.GinEngine.Run(serverAddr); err != nil {
			klog.Fatalf("Failed to start API server: %s", err.Error())
		}
	}()

	<-ctx.Done()

	return nil
}

func (a *App) synchronizeCurrentReleaseInfo() error {
	// The source-tree default is not a published control-plane release. It
	// deliberately has no ReleaseInfo baseline to seed.
	if a.config.Version == "dev" {
		klog.Info("Skipping ReleaseInfo seed for development build")
		return nil
	}

	if _, err := a.synchronizeReleaseInfo(a.config.Storage, a.config.Version); err != nil {
		return fmt.Errorf("synchronize release info: %w", err)
	}

	return nil
}
