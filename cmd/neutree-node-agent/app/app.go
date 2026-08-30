package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"runtime"

	"github.com/spf13/pflag"
	"k8s.io/klog/v2"

	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics"
)

// BuildInfo contains the metadata printed by the version command.
type BuildInfo struct {
	Version   string
	GitCommit string
	BuildTime string
}

// App is the private NodeAgent application assembled by the entrypoint.
type App struct {
	options         *options
	args            []string
	build           BuildInfo
	registry        adapterRegistry
	flagsRegistered bool
}

// AddFlags registers NodeAgent flags on the caller-owned flag set. New binary
// entrypoints should call this from main before parsing their command line.
func (a *App) AddFlags(fs *pflag.FlagSet) {
	if a.options == nil {
		a.options = newOptions()
	}

	a.options.addFlags(fs)

	a.flagsRegistered = true
}

// runOptions returns the application-owned options. WithArgs is retained for
// older embedders and parses into that same object; mixing it with AddFlags is
// rejected because the caller-owned flag set is already authoritative.
func (a *App) runOptions() (*options, error) {
	if a.options == nil {
		a.options = newOptions()
	}

	if a.flagsRegistered {
		if len(a.args) > 0 {
			return nil, fmt.Errorf("WithArgs cannot be combined with AddFlags")
		}

		return a.options, nil
	}

	if len(a.args) == 0 {
		return a.options, nil
	}

	flags := pflag.NewFlagSet("neutree-node-agent", pflag.ContinueOnError)
	a.options.addFlags(flags)
	flags.AddGoFlagSet(flag.CommandLine)

	err := flags.Parse(a.args)
	if err != nil {
		return nil, err
	}

	return a.options, nil
}

// Run starts the NodeAgent using options parsed by the entrypoint. A legacy
// WithArgs caller is parsed only when AddFlags was not used.
func (a *App) Run(ctx context.Context) error {
	if isVersionCommand(a.args) {
		fmt.Println(formatBuildInfo(a.build))

		return nil
	}

	opts, err := a.runOptions()
	if errors.Is(err, pflag.ErrHelp) {
		return nil
	}

	if err != nil {
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
