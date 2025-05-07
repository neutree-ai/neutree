package launch

import (
	"os"
	"path/filepath"

	"github.com/compose-spec/compose-go/cli"
	"github.com/compose-spec/compose-go/loader"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"

	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/util"
	"github.com/neutree-ai/neutree/pkg/command"
)

type commonOptions struct {
	workDir string

	deployType string
	deployMode string
	deployIps  []string
	nodeIP     string

	mirrorRegistry string
}

func NewLaunchCmd() *cobra.Command {
	commonOptions := &commonOptions{
		workDir: launchWorkDir(),
	}

	launchCmd := &cobra.Command{
		Use:   "launch",
		Short: "Deploy Neutree components",
		Long: `The launch command provides an easy way to install and deploy Neutree related components.
		
Available deployment types and modes:
  - deploy-type: local (default), cloud, hybrid
  - deploy-mode: single (default), cluster

Common options:
  --deploy-ips       Specify target IP addresses for deployment
  --mirror-registry  Use a custom image registry mirror
  --node-ip          Specify the current node IP address for deployment


Subcommands:
  obs-stack      Install Neutree OBS storage stack
  neutree-core   Install Neutree core services

Examples:
  # Local single node deployment
  neutree-cli launch --deploy-type local
  
  # Cluster deployment with custom registry
  neutree-cli launch --deploy-type cloud --deploy-mode cluster \
    --deploy-ips 192.168.1.10,192.168.1.11 \
    --mirror-registry my.registry.com`,
	}

	launchCmd.PersistentFlags().StringVar(&commonOptions.deployType, "deploy-type", "local", "deploy type")
	launchCmd.PersistentFlags().StringVar(&commonOptions.deployMode, "deploy-mode", "single", "deploy mode")
	launchCmd.PersistentFlags().StringSliceVar(&commonOptions.deployIps, "deploy-ips", []string{}, "deploy ips")
	launchCmd.PersistentFlags().StringVar(&commonOptions.nodeIP, "node-ip", "", "current deploy node ip")

	launchCmd.PersistentFlags().StringVar(&commonOptions.mirrorRegistry, "mirror-registry", "", "mirror registry")

	exector := &command.OSExecutor{}
	launchCmd.AddCommand(NewObsStackInstallCmd(exector, commonOptions))
	launchCmd.AddCommand(NewNeutreeCoreInstallCmd(exector, commonOptions))

	return launchCmd
}

func launchWorkDir() string {
	if os.Getenv("NEUTREE_LAUNCH_WORK_DIR") != "" {
		return os.Getenv("NEUTREE_LAUNCH_WORK_DIR")
	}

	currentDir, _ := os.Getwd()

	return filepath.Join(currentDir, "neutree-deploy")
}

func replaceComposeImageRegistry(composeFile string, mirrorRegistry string) error {
	if mirrorRegistry == "" {
		return nil
	}

	options, err := cli.NewProjectOptions([]string{composeFile}, cli.WithLoadOptions(func(o *loader.Options) {
		// disable interpolation to avoid character escapes
		o.SkipInterpolation = true
	}))
	if err != nil {
		return errors.Wrapf(err, "load docker compose file %s error", composeFile)
	}

	project, err := cli.ProjectFromOptions(options)
	if err != nil {
		return errors.Wrapf(err, "analyze docker compose file %s error", composeFile)
	}

	for serviceName, service := range project.Services {
		newImage, err := util.ReplaceImageRegistry(service.Image, mirrorRegistry)
		if err != nil {
			return errors.Wrapf(err, "replace image registry %s error", service.Image)
		}

		project.Services[serviceName].Image = newImage
	}

	data, err := project.MarshalYAML()
	if err != nil {
		return errors.Wrapf(err, "marshal docker compose file %s error", composeFile)
	}

	err = os.WriteFile(composeFile, data, 0600)
	if err != nil {
		return errors.Wrapf(err, "write docker compose file %s error", composeFile)
	}

	return nil
}
