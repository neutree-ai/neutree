package app

import (
	"context"
	"fmt"

	"k8s.io/klog/v2"

	"github.com/neutree-ai/neutree/cmd/neutree-core/app/config"
	"github.com/neutree-ai/neutree/controllers"
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

	go a.config.ObsCollectConfigManager.Start(ctx)

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
