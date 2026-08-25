package app

import (
	"context"
	"flag"
	"fmt"
	"runtime"

	"github.com/spf13/pflag"
	"k8s.io/klog/v2"

	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics"
)

// BuildInfo provides the build metadata printed by the version command.
type BuildInfo struct {
	Version   string
	GitCommit string
	BuildTime string
}

// App is the private NodeAgent application assembled by the entrypoint.
type App struct {
	args     []string
	build    BuildInfo
	registry adapterRegistry
}

// Run starts the NodeAgent using the Builder-assembled configuration.
func (a *App) Run(ctx context.Context) error {
	if isVersionCommand(a.args) {
		fmt.Println(formatBuildInfo(a.build))

		return nil
	}

	klog.InitFlags(nil)

	if err := flag.Set("v", "2"); err != nil {
		return fmt.Errorf("set default log verbosity: %w", err)
	}

	defer klog.Flush()

	opts := newOptions()
	flags := pflag.NewFlagSet("neutree-node-agent", pflag.ContinueOnError)
	opts.addFlags(flags)
	flags.AddGoFlagSet(flag.CommandLine)

	if err := flags.Parse(a.args); err != nil {
		return err
	}

	serverConfig, err := opts.configWithRegistry(a.registry)
	if err != nil {
		return fmt.Errorf("build neutree-node-agent config: %w", err)
	}

	klog.V(2).InfoS(
		"Built neutree-node-agent config",
		"listen_address", opts.listenAddress,
		"cluster_type", opts.clusterType,
		"metrics_mode", opts.metricsMode,
		"node", opts.node,
		"node_ip", opts.nodeIP,
	)

	server, err := neutreemetrics.NewServer(serverConfig)
	if err != nil {
		return fmt.Errorf("create neutree-node-agent server: %w", err)
	}

	return server.Run(ctx)
}

func isVersionCommand(args []string) bool {
	return len(args) == 1 && (args[0] == "version" || args[0] == "--version")
}

func formatBuildInfo(info BuildInfo) string {
	return fmt.Sprintf(
		"Version: %s\nGit Commit: %s\nBuild Time: %s\nGo Version: %s\nPlatform: %s/%s",
		info.Version,
		info.GitCommit,
		info.BuildTime,
		runtime.Version(),
		runtime.GOOS,
		runtime.GOARCH,
	)
}
