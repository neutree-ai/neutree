package nodeagent

import (
	"context"
	"flag"
	"fmt"
	"runtime"

	"github.com/spf13/pflag"
	"k8s.io/klog/v2"

	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics"
	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

// BuildInfo provides the build metadata printed by the version command.
type BuildInfo struct {
	Version   string
	GitCommit string
	BuildTime string
}

// Config is the public NodeAgent host configuration. The entrypoint owns the
// complete adapter slice and passes it explicitly to Run.
type Config struct {
	Args     []string
	Build    BuildInfo
	Adapters []adapter.Accelerator
}

// Run starts the NodeAgent using the explicitly supplied adapters.
func Run(ctx context.Context, config Config) error {
	registry, err := newAdapterRegistry(config.Adapters)
	if err != nil {
		return fmt.Errorf("build accelerator adapter registry: %w", err)
	}
	if err := neutreemetrics.ValidateAdapterMetricDescriptors(registry.descriptorsCopy()); err != nil {
		return fmt.Errorf("validate accelerator adapter descriptors: %w", err)
	}

	if isVersionCommand(config.Args) {
		fmt.Println(formatBuildInfo(config.Build))

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
	if err := flags.Parse(config.Args); err != nil {
		return err
	}
	if err := validateSelectedAdapterCapability(opts.clusterType, opts.acceleratorType, registry); err != nil {
		return err
	}

	serverConfig, err := opts.configWithRegistry(registry)
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

func validateSelectedAdapterCapability(clusterType, acceleratorType string, registry adapterRegistry) error {
	if acceleratorType == "" {
		return nil
	}

	accelerator, ok := registry.byType[acceleratorType]
	if !ok {
		return fmt.Errorf(
			"accelerator adapter %q is not registered; available adapters: %s",
			acceleratorType,
			registeredAdapterTypes(registry),
		)
	}

	switch clusterType {
	case clusterTypeKubernetes:
		if _, ok := accelerator.(adapter.KubernetesAccelerator); !ok {
			return fmt.Errorf("accelerator adapter %q does not implement Kubernetes capability", acceleratorType)
		}
	case clusterTypeRay:
		if _, ok := accelerator.(adapter.StaticAccelerator); !ok {
			return fmt.Errorf("accelerator adapter %q does not implement static capability", acceleratorType)
		}
	default:
		return fmt.Errorf("cluster type %q is unsupported for accelerator adapter dispatch", clusterType)
	}

	return nil
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
