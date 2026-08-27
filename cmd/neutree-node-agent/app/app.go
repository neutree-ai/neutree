package app

import (
	"context"
	"fmt"

	"github.com/spf13/pflag"
	"k8s.io/klog/v2"

	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics"
)

// App is the private NodeAgent application assembled by the entrypoint.
type App struct {
	options  *options
	registry adapterRegistry
}

// AddFlags registers the NodeAgent flags on the caller-owned command line.
func (a *App) AddFlags(fs *pflag.FlagSet) {
	a.options.addFlags(fs)
}

// Run starts the NodeAgent using the entrypoint-parsed configuration.
func (a *App) Run(ctx context.Context) error {
	serverConfig, err := a.options.configWithRegistry(a.registry)
	if err != nil {
		return fmt.Errorf("build neutree-node-agent config: %w", err)
	}

	klog.V(2).InfoS(
		"Built neutree-node-agent config",
		"listen_address", a.options.listenAddress,
		"cluster_type", a.options.clusterType,
		"node", a.options.node,
		"node_ip", a.options.nodeIP,
	)

	server, err := neutreemetrics.NewServer(serverConfig)
	if err != nil {
		return fmt.Errorf("create neutree-node-agent server: %w", err)
	}

	return server.Run(ctx)
}
