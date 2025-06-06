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
	"github.com/neutree-ai/neutree/pkg/storage"
)

type neutreeCoreInstallOptions struct {
	*commonOptions

	jwtSecret             string
	metricsRemoteWriteURL string
	grafanaURL            string
	version               string
}

func NewNeutreeCoreInstallCmd(exector command.Executor, commonOptions *commonOptions) *cobra.Command {
	options := neutreeCoreInstallOptions{
		commonOptions: commonOptions,
	}

	neutreeCoreInstallCmd := &cobra.Command{
		Use:   "neutree-core",
		Short: "Install Neutree Core Services",
		Long: `Install and configure the core components of Neutree platform.

Components Included:
  • Core services
  • Database initialization
  • Metrics collection system

Configuration Options:
  --jwt-secret             JWT secret for authentication
  --metrics-remote-write-url Remote metrics storage URL
  --grafana-url            Grafana dashboard URL for system info API
  --version                Component version (default: v0.0.1)

Examples:
  # Basic installation
  neutree-cli launch neutree-core
  
  # Custom version installation
  neutree-cli launch neutree-core --version v1.2.0
  
  # With remote metrics storage and Grafana
  neutree-cli launch neutree-core --metrics-remote-write-url http://metrics.example.com --grafana-url http://grafana.example.com:3030`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// set default node ip
			if options.nodeIP == "" {
				ip, err := util.GetHostIP()
				if err != nil {
					return err
				}
				options.nodeIP = ip
			}
			return installNeutreeCore(exector, options)
		},
	}

	neutreeCoreInstallCmd.PersistentFlags().StringVar(&options.jwtSecret, "jwt-secret", "mDCvM4zSk0ghmpyKhgqWb0g4igcOP0Lp", "neutree core jwt secret")
	neutreeCoreInstallCmd.PersistentFlags().StringVar(&options.metricsRemoteWriteURL, "metrics-remote-write-url", "", "metrics remote write url")
	neutreeCoreInstallCmd.PersistentFlags().StringVar(&options.grafanaURL, "grafana-url", "", "grafana dashboard url for system info API")
	neutreeCoreInstallCmd.PersistentFlags().StringVar(&options.version, "version", "v0.0.1", "neutree core version")

	return neutreeCoreInstallCmd
}

func installNeutreeCore(exector command.Executor, options neutreeCoreInstallOptions) error {
	switch options.deployType {
	case constants.DeployTypeLocal:
		return installNeutreeCoreByDocker(exector, options)
	default:
		return fmt.Errorf("unsupported deploy type: %s", options.deployType)
	}
}

func installNeutreeCoreByDocker(exector command.Executor, options neutreeCoreInstallOptions) error {
	switch options.deployMode {
	case constants.DeployModeSingle:
		return installNeutreeCoreSingleNodeByDocker(exector, options)
	default:
		return fmt.Errorf("unsupported deploy mode: %s", options.deployMode)
	}
}

func installNeutreeCoreSingleNodeByDocker(exector command.Executor, options neutreeCoreInstallOptions) error {
	err := prepareNeutreeCoreDeployConfig(options)
	if err != nil {
		return errors.Wrap(err, "prepare neutree core launch config failed")
	}

	if options.dryRun {
		fmt.Println("dry run, skip install neutree core")

		composeContent, err := os.ReadFile(filepath.Join(options.workDir, "neutree-core", "docker-compose.yml"))
		if err != nil {
			return errors.Wrap(err, "read docker compose file failed")
		}

		fmt.Println(string(composeContent))

		return nil
	}

	output, err := exector.Execute(context.Background(), "docker",
		[]string{"compose", "-p", "neutree-core", "-f", filepath.Join(options.workDir, "neutree-core", "docker-compose.yml"), "up", "-d"})
	if err != nil {
		return errors.Wrapf(err, "error when executing docker compose up, failed msg %s", string(output))
	}

	return nil
}

func prepareNeutreeCoreDeployConfig(options neutreeCoreInstallOptions) error {
	// extract neutree core deploy manifests
	neutreeCoreDeployManifestsTarFile, err := manifests.NeutreeDeployManifestsTar.Open("neutree-core.tar")
	if err != nil {
		return errors.Wrap(err, "open neutree core db init scripts failed")
	}
	defer neutreeCoreDeployManifestsTarFile.Close()

	err = util.ExtractTar(neutreeCoreDeployManifestsTarFile, options.workDir)
	if err != nil {
		return errors.Wrap(err, "extract neutree core db init scripts failed")
	}

	coreWorkDir := filepath.Join(options.workDir, "neutree-core")

	// extract db init scripts
	neutreeDBInitScriptsTarFile, err := manifests.NeutreeCoreDBInitScriptsTar.Open("db.tar")
	if err != nil {
		return errors.Wrap(err, "open neutree core db init scripts failed")
	}
	defer neutreeDBInitScriptsTarFile.Close()

	err = util.ExtractTar(neutreeDBInitScriptsTarFile, coreWorkDir)
	if err != nil {
		return errors.Wrap(err, "extract neutree core db init scripts failed")
	}

	// parseTemplate
	tempplateFiles := []string{
		filepath.Join(coreWorkDir, "docker-compose.yml"),
		filepath.Join(coreWorkDir, "vector", "vector.yml"),
	}

	jwtToken, err := storage.CreateServiceToken(options.jwtSecret)
	if err != nil {
		return errors.Wrap(err, "create jwt token failed")
	}

	templateParams := map[string]string{
		"NeutreeCoreWorkDir":     coreWorkDir,
		"JwtSecret":              options.jwtSecret,
		"MetricsRemoteWriteURL":  options.metricsRemoteWriteURL,
		"GrafanaURL":             options.grafanaURL,
		"VictoriaMetricsVersion": constants.VictoriaMetricsVersion,
		"NeutreeCoreVersion":     options.version,
		"NeutreeAPIVersion":      options.version,
		"JwtToken":               *jwtToken,
		"VectorVersion":          constants.VectorVersion,
		"KongVersion":            constants.KongVersion,
		"NodeIP":                 options.nodeIP,
	}

	err = util.BatchParseTemplateFiles(tempplateFiles, templateParams)
	if err != nil {
		return errors.Wrap(err, "parse template files failed")
	}

	composeFilePath := filepath.Join(coreWorkDir, "docker-compose.yml")
	err = replaceComposeImageRegistry(composeFilePath, options.mirrorRegistry)

	if err != nil {
		return errors.Wrapf(err, "replace compose image registry failed, file path: %s", composeFilePath)
	}

	return nil
}
