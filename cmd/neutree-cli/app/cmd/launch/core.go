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

type neutreeCoreInstallOptions struct {
	*commonOptions

	jwtSecret             string
	metricsRemoteWriteURL string
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
  --version                Component version (default: v0.0.1)

Examples:
  # Basic installation
  neutree-cli launch neutree-core
  
  # Custom version installation
  neutree-cli launch neutree-core --version v1.2.0
  
  # With remote metrics storage
  neutree-cli launch neutree-core --metrics-remote-write-url http://metrics.example.com`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return installNeutreeCore(exector, options)
		},
	}

	neutreeCoreInstallCmd.PersistentFlags().StringVar(&options.jwtSecret, "jwt-secret", "mDCvM4zSk0ghmpyKhgqWb0g4igcOP0Lp", "neutree core jwt secret")
	neutreeCoreInstallCmd.PersistentFlags().StringVar(&options.metricsRemoteWriteURL, "metrics-remote-write-url", "", "metrics remote write url")
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
	coreWorkDir := filepath.Join(options.workDir, "neutree-core")

	err := os.MkdirAll(coreWorkDir, 0755)
	if err != nil {
		return errors.Wrap(err, "create neutree core work dir failed")
	}

	err = prepareNeutreeCoreConfig(coreWorkDir, options)
	if err != nil {
		return errors.Wrap(err, "prepare neutree core config failed")
	}

	neutreeCoreComposeContent, err := util.ParseTemplate(manifests.NeutreeCoreDockerComposeManifests, map[string]string{
		"WorkDir":                coreWorkDir,
		"JwtSecret":              options.jwtSecret,
		"MetricsRemoteWriteURL":  options.metricsRemoteWriteURL,
		"VictoriaMetricsVersion": constants.VictoriaMetricsVersion,
		"NeutreeCoreVersion":     options.version,
	})

	if err != nil {
		return errors.Wrap(err, "parse neutree core docker compose manifests failed")
	}

	composeFilePath := filepath.Join(coreWorkDir, "docker-compose.yaml")

	err = os.WriteFile(composeFilePath, neutreeCoreComposeContent, 0600)
	if err != nil {
		return errors.Wrap(err, "write neutree core docker compose manifests failed")
	}

	err = replaceComposeImageRegistry(composeFilePath, options.mirrorRegistry)
	if err != nil {
		return errors.Wrapf(err, "replace compose image registry failed, file path: %s", composeFilePath)
	}

	output, err := exector.Execute(context.Background(), "docker",
		[]string{"compose", "-p", "neutree-core", "-f", filepath.Join(coreWorkDir, "docker-compose.yaml"), "up", "-d"})
	if err != nil {
		return errors.Wrapf(err, "error when executing docker compose up, failed msg %s", string(output))
	}

	return nil
}

func prepareNeutreeCoreConfig(workDir string, _ neutreeCoreInstallOptions) error {
	// write prometheus scrape config
	metricsConfigDir := filepath.Join(workDir, "metrics")

	err := os.MkdirAll(metricsConfigDir, 0755)
	if err != nil {
		return errors.Wrap(err, "create metrics config dir failed")
	}

	err = os.WriteFile(filepath.Join(metricsConfigDir, "prometheus-cluster.yml"), []byte(manifests.NeutreePrometheusScrapeConfig), 0644) //nolint:gosec
	if err != nil {
		return errors.Wrap(err, "write neutree prometheus scrape config failed")
	}

	// extract db init scripts
	neutreeDBInitScriptsTarFile, err := manifests.NeutreeCoreDBInitScripts.Open("db.tar")
	if err != nil {
		return errors.Wrap(err, "open neutree core db init scripts failed")
	}
	defer neutreeDBInitScriptsTarFile.Close()

	err = util.ExtractTar(neutreeDBInitScriptsTarFile, workDir)
	if err != nil {
		return errors.Wrap(err, "extract neutree core db init scripts failed")
	}

	return nil
}
