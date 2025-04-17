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
	ip, err := util.GetHostIP()
	if err != nil {
		return err
	}

	switch options.deployMode {
	case constants.DeployModeSingle:
		return installObsStackSingleNodeByDocker(exector, options, ip)
	default:
		return fmt.Errorf("unsupported deploy mode: %s", options.deployMode)
	}
}

func installObsStackSingleNodeByDocker(exector command.Executor, options obsStackInstallOptions, hostIP string) error {
	// install victoriametrics
	if err := installVictoriaMetricsSingleNodeByDocker(exector, options.commonOptions, hostIP); err != nil {
		return errors.Wrapf(err, "install victoriametrics failed")
	}

	// install grafana
	if err := installGrafanaSingleNodeByDocker(exector, options.commonOptions, hostIP); err != nil {
		return errors.Wrapf(err, "install grafana failed")
	}

	return nil
}

func installVictoriaMetricsSingleNodeByDocker(exector command.Executor, options *commonOptions, hostIP string) error {
	vmWorkDir := filepath.Join(options.workDir, "victoriametrics")

	err := os.MkdirAll(vmWorkDir, 0755)
	if err != nil {
		return errors.Wrap(err, "create victoriametrics work dir failed")
	}

	composeSpecContent, err := util.ParseTemplate(manifests.VictoriaMetricsDockerComposeManifests, struct {
		DeployIps              []string
		VictoriaMetricsVersion string
	}{
		DeployIps:              []string{hostIP},
		VictoriaMetricsVersion: constants.VictoriaMetricsClusterVersion,
	})

	if err != nil {
		return errors.Wrapf(err, "parse victoriametrics compose file failed")
	}

	composeFilePath := filepath.Join(vmWorkDir, "docker-compose.yaml")

	err = os.WriteFile(composeFilePath, composeSpecContent, 0600)
	if err != nil {
		return errors.Wrapf(err, "write victoriametrics compose file failed, file path: %s", composeFilePath)
	}

	err = replaceComposeImageRegistry(composeFilePath, options.mirrorRegistry)
	if err != nil {
		return errors.Wrapf(err, "replace compose image registry failed, file path: %s", composeFilePath)
	}

	output, err := exector.Execute(context.Background(), "docker",
		[]string{"compose", "-p", "victoriametrics", "-f", composeFilePath, "up", "-d"})
	if err != nil {
		return errors.Wrapf(err, "error when executing docker compose up, failed msg %s", string(output))
	}

	return nil
}

func installGrafanaSingleNodeByDocker(exector command.Executor, options *commonOptions, hostIP string) error {
	grafanaWorkDir := filepath.Join(options.workDir, "grafana")

	err := os.MkdirAll(grafanaWorkDir, 0755)
	if err != nil {
		return errors.Wrap(err, "create grafana work dir failed")
	}

	err = prepareGrafanaConfig(grafanaWorkDir, hostIP)
	if err != nil {
		return errors.Wrap(err, "prepare grafana config failed")
	}

	grafanaComposeContent, err := util.ParseTemplate(manifests.GrafanaDockerComposeManifests, map[string]string{
		"WorkDir":        grafanaWorkDir,
		"GrafanaVersion": constants.GrafanaVersion,
	})
	if err != nil {
		return errors.Wrap(err, "parse grafana compose file failed")
	}

	composeFilePath := filepath.Join(grafanaWorkDir, "docker-compose.yaml")

	err = os.WriteFile(composeFilePath, grafanaComposeContent, 0600)
	if err != nil {
		return errors.Wrapf(err, "write grafana compose file failed, file path: %s", composeFilePath)
	}

	err = replaceComposeImageRegistry(composeFilePath, options.mirrorRegistry)
	if err != nil {
		return errors.Wrapf(err, "replace compose image registry failed, file path: %s", composeFilePath)
	}

	output, err := exector.Execute(context.Background(), "docker",
		[]string{"compose", "-p", "grafana", "-f", composeFilePath, "up", "-d"})
	if err != nil {
		return errors.Wrapf(err, "error when executing docker compose up, failed msg %s", string(output))
	}

	return nil
}

func prepareGrafanaConfig(workDir string, hostIP string) error {
	datasourceConfigPath := filepath.Join(workDir, "provisioning", "datasources")
	dashboardConfigPath := filepath.Join(workDir, "provisioning", "dashboards")
	rayDashboardPath := filepath.Join(workDir, "dashboards")

	initDirs := []string{
		datasourceConfigPath,
		dashboardConfigPath,
		rayDashboardPath,
	}

	for _, dir := range initDirs {
		err := os.MkdirAll(dir, 0755)
		if err != nil {
			return errors.Wrap(err, "create grafana config work dir failed")
		}
	}

	grafanaDataSourceContent, err := util.ParseTemplate(manifests.GrafanaDatasource, map[string]string{
		"Ip": hostIP,
	})
	if err != nil {
		return errors.Wrap(err, "parse grafana datasource failed")
	}

	fileWriterList := []struct {
		filename string
		content  []byte
	}{
		{
			filename: filepath.Join(workDir, "grafana.ini"),
			content:  []byte(manifests.GrafanaConfig),
		},
		{
			filename: filepath.Join(datasourceConfigPath, "cluster.yml"),
			content:  grafanaDataSourceContent,
		},
		{
			filename: filepath.Join(dashboardConfigPath, "dashboard.yml"),
			content:  []byte(manifests.GrafanaDashboardConfig),
		},
	}

	for _, file := range fileWriterList {
		err = os.WriteFile(file.filename, file.content, 0644) //nolint:gosec
		if err != nil {
			return errors.Wrap(err, "write grafana file failed")
		}
	}

	// write dashboard file
	dashboardTarFile, err := manifests.GrafanaDashboardContent.Open("dashboards.tar")
	if err != nil {
		return errors.Wrap(err, "open grafana dashboard tar file failed")
	}
	defer dashboardTarFile.Close()

	err = util.ExtractTar(dashboardTarFile, workDir)
	if err != nil {
		return errors.Wrap(err, "extract grafana dashboard tar file failed")
	}

	return nil
}
