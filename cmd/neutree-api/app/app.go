package app

import (
	"context"
	"fmt"

	"k8s.io/klog/v2"

	"github.com/neutree-ai/neutree/cmd/neutree-api/app/config"
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

	go func() {
		if err := a.config.GinEngine.Run(serverAddr); err != nil {
			klog.Fatalf("Failed to start API server: %s", err.Error())
		}
	}()

	<-ctx.Done()

	return nil
}
