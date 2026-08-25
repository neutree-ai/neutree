// Package nodeagent exposes the public NodeAgent host facade for accelerator
// adapters implemented in another Go module.
package nodeagent

import (
	"context"
	"fmt"

	nodeagentapp "github.com/neutree-ai/neutree/cmd/neutree-node-agent/app"
	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

// BuildInfo provides build metadata printed by the NodeAgent version command.
type BuildInfo struct {
	Version   string
	GitCommit string
	BuildTime string
}

// Config contains the entrypoint-owned inputs for a NodeAgent process.
type Config struct {
	Args     []string
	Build    BuildInfo
	Adapters []adapter.Accelerator
}

// DefaultAdapters returns a new slice for the Community default adapter set.
// NVIDIA is added by its migration in the next stacked change, while this
// baseline preserves the existing legacy DCGM path for an empty adapter set.
func DefaultAdapters() []adapter.Accelerator {
	return []adapter.Accelerator{}
}

// Run validates the explicit adapter set and starts the NodeAgent host.
func Run(ctx context.Context, config Config) error {
	application, err := nodeagentapp.NewBuilder().
		WithArgs(config.Args).
		WithBuildInfo(nodeagentapp.BuildInfo{
			Version:   config.Build.Version,
			GitCommit: config.Build.GitCommit,
			BuildTime: config.Build.BuildTime,
		}).
		WithAdapters(config.Adapters...).
		Build()
	if err != nil {
		return fmt.Errorf("build node-agent application: %w", err)
	}

	return application.Run(ctx)
}
