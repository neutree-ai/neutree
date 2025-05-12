package launch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"

	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/cmd/launch/manifests"
	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/constants"
	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/util"
	"github.com/neutree-ai/neutree/pkg/command"
)

type obsStackInstallOptions struct {
	*commonOptions
}

func NewObsStackInstallCmd(exector command.Executor, commonOptions *commonOptions) *cobra.Command {
	options := obsStackInstallOptions{
		commonOptions: commonOptions,
	}

	installObsStackCmd := &cobra.Command{
		Use:   "obs-stack",
		Short: "Deploy Neutree Observability Stack",
		Long: `Deploy the Neutree Observability Stack including VictoriaMetrics and Grafana.

Components:
  • VictoriaMetrics: Time-series database for metrics storage
  • Grafana: Visualization platform for observability data

Deployment Options:
  • Local single-node deployment (default)
  
Examples:
  # Deploy with default settings
  neutree-cli launch obs-stack
  
  # Deploy with custom registry
  neutree-cli launch obs-stack --mirror-registry my.registry.com`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// set default node ip
			if options.nodeIP == "" {
				ip, err := util.GetHostIP()
				if err != nil {
					return err
				}
				options.nodeIP = ip
			}

			return installObsStack(exector, options)
		},
	}

	return installObsStackCmd
}

func installObsStack(exector command.Executor, options obsStackInstallOptions) error {
	switch options.deployType {
	case constants.DeployTypeLocal:
		return installObsStackByDocker(exector, options)
	default:
		return fmt.Errorf("unsupported deploy type: %s", options.deployType)
	}
}

func installObsStackByDocker(exector command.Executor, options obsStackInstallOptions) error {
	switch options.deployMode {
	case constants.DeployModeSingle:
		return installObsStackSingleNodeByDocker(exector, options)
	default:
		return fmt.Errorf("unsupported deploy mode: %s", options.deployMode)
	}
}

func installObsStackSingleNodeByDocker(exector command.Executor, options obsStackInstallOptions) error {
	err := prepareObsStackDeployConfig(&options)
	if err != nil {
		return errors.Wrap(err, "prepare obs stack deploy config failed")
	}

	if options.dryRun {
		fmt.Println("dry run, skip install obs stack")

		composeContent, err := os.ReadFile(filepath.Join(options.workDir, "obs-stack", "docker-compose.yml"))
		if err != nil {
			return errors.Wrap(err, "read docker compose file failed")
		}

		fmt.Println(string(composeContent))
	}

	output, err := exector.Execute(context.Background(), "docker",
		[]string{"compose", "-p", "obs-stack", "-f", filepath.Join(options.workDir, "obs-stack", "docker-compose.yml"), "up", "-d"})
	if err != nil {
		return errors.Wrapf(err, "error when executing docker compose up, failed msg %s", string(output))
	}

	return nil
}

func prepareObsStackDeployConfig(options *obsStackInstallOptions) error {
	// extract neutree core deploy manifests
	obsStackDeployManifestsTarFile, err := manifests.ObsStackDeployManifestsTar.Open("obs-stack.tar")
	if err != nil {
		return errors.Wrap(err, "open obs stack deploy manifests tar file failed")
	}
	defer obsStackDeployManifestsTarFile.Close()

	err = util.ExtractTar(obsStackDeployManifestsTarFile, options.workDir)
	if err != nil {
		return errors.Wrap(err, "extract obs stack deploy manifests tar file failed")
	}

	templateFiles := []string{
		filepath.Join(options.workDir, "obs-stack", "docker-compose.yml"),
		filepath.Join(options.workDir, "obs-stack", "grafana", "provisioning", "datasources", "cluster.yml"),
	}

	// single deploy mode
	if len(options.deployIps) == 0 {
		options.deployIps = []string{options.nodeIP}
	}

	templateParams := struct {
		DeployIps              []string
		NodeIP                 string
		VictoriaMetricsVersion string
		GrafanaVersion         string
		GrafanaWorkDir         string
	}{
		DeployIps:              options.deployIps,
		VictoriaMetricsVersion: constants.VictoriaMetricsClusterVersion,
		NodeIP:                 options.nodeIP,
		GrafanaVersion:         constants.GrafanaVersion,
		GrafanaWorkDir:         filepath.Join(options.workDir, "obs-stack", "grafana"),
	}

	err = util.BatchParseTemplateFiles(templateFiles, templateParams)
	if err != nil {
		return errors.Wrap(err, "batch parse template files failed")
	}

	composeFilePath := filepath.Join(options.workDir, "obs-stack", "docker-compose.yml")
	err = replaceComposeImageRegistry(composeFilePath, options.mirrorRegistry)

	if err != nil {
		return errors.Wrapf(err, "replace compose image registry failed, file path: %s", composeFilePath)
	}

	return nil
}
