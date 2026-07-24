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
	"github.com/neutree-ai/neutree/internal/componentversion"
	"github.com/neutree-ai/neutree/pkg/command"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type neutreeCoreInstallOptions struct {
	*commonOptions

	jwtSecret             string
	dbPassword            string
	metricsRemoteWriteURL string
	grafanaURL            string
	version               string
	adminPassword         string

	victorialogsRetentionPeriod string
}

// defaultVictoriaLogsRetentionPeriod is the default VictoriaLogs log retention
// period rendered into the neutree-core compose file. Overridable at deploy time
// via --victorialogs-retention-period.
const defaultVictoriaLogsRetentionPeriod = "30d"

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
  --version                Component version (default: CLI release version, or v0.0.1 for non-release/local builds)
  --victorialogs-retention-period VictoriaLogs log retention period (default: 30d; e.g. 30d, 90d, 1y)

Examples:
  # Basic installation
  neutree-cli launch neutree-core

  # Compatible version installation
  neutree-cli launch neutree-core --version <compatible-version-for-your-cli-release-line>

  # With remote metrics storage and Grafana
  neutree-cli launch neutree-core --metrics-remote-write-url http://metrics.example.com --grafana-url http://grafana.example.com:3030`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if options.jwtSecret == "" {
				return fmt.Errorf("--jwt-secret is required")
			}

			err := resolveNodeIP(cmd.OutOrStdout(), options.commonOptions, util.GetHostIP)
			if err != nil {
				return err
			}

			return installNeutreeCore(exector, options)
		},
	}

	neutreeCoreInstallCmd.PersistentFlags().StringVar(&options.jwtSecret, "jwt-secret", "", "neutree core jwt secret (required)")
	neutreeCoreInstallCmd.PersistentFlags().StringVar(&options.dbPassword, "db-password", "pgpassword", "database password for postgres superuser")
	neutreeCoreInstallCmd.PersistentFlags().StringVar(&options.metricsRemoteWriteURL, "metrics-remote-write-url", "", "metrics remote write url")
	neutreeCoreInstallCmd.PersistentFlags().StringVar(&options.grafanaURL, "grafana-url", "", "grafana dashboard url for system info API")
	neutreeCoreInstallCmd.PersistentFlags().StringVar(&options.version, "version", defaultNeutreeCoreVersion(), "neutree core version")
	neutreeCoreInstallCmd.PersistentFlags().StringVar(&options.victorialogsRetentionPeriod, "victorialogs-retention-period",
		defaultVictoriaLogsRetentionPeriod, "VictoriaLogs log retention period (e.g. 30d, 90d, 1y)")
	neutreeCoreInstallCmd.PersistentFlags().StringVar(&options.adminPassword, "admin-password", "", "the password for the neutree admin user."+
		"it is valid when starting neutree core for the first time. "+
		"It is recommended to change it quickly after installation.")

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
	if err := validateNeutreeCoreVersionCompatibility(getCLIAppVersion(), options.version); err != nil {
		return err
	}

	if options.dryRun {
		tempWorkDir, err := os.MkdirTemp("", "neutree-core-dry-run-")
		if err != nil {
			return errors.Wrap(err, "create dry-run work dir failed")
		}
		defer os.RemoveAll(tempWorkDir)

		err = prepareNeutreeCoreDeployConfigInWorkDir(options, tempWorkDir)
		if err != nil {
			return errors.Wrap(err, "prepare neutree core launch config failed")
		}

		fmt.Println("dry run, skip install neutree core")

		composeContent, err := os.ReadFile(filepath.Join(tempWorkDir, "neutree-core", "docker-compose.yml"))
		if err != nil {
			return errors.Wrap(err, "read docker compose file failed")
		}

		fmt.Println(string(composeContent))

		return nil
	}

	err := prepareNeutreeCoreDeployConfig(options)
	if err != nil {
		return errors.Wrap(err, "prepare neutree core launch config failed")
	}

	output, err := exector.Execute(context.Background(), "docker",
		[]string{"compose", "-p", "neutree-core", "-f", filepath.Join(options.workDir, "neutree-core", "docker-compose.yml"), "up", "-d"})
	if err != nil {
		return errors.Wrapf(err, "error when executing docker compose up, failed msg %s", string(output))
	}

	return nil
}

func prepareNeutreeCoreDeployConfig(options neutreeCoreInstallOptions) error {
	return prepareNeutreeCoreDeployConfigInWorkDir(options, options.workDir)
}

func prepareNeutreeCoreDeployConfigInWorkDir(options neutreeCoreInstallOptions, outputWorkDir string) error {
	renderedCoreWorkDir := filepath.Join(outputWorkDir, "neutree-core")

	// extract neutree core deploy manifests
	neutreeCoreDeployManifestsTarFile, err := manifests.NeutreeDeployManifestsTar.Open("neutree-core.tar")
	if err != nil {
		return errors.Wrap(err, "open neutree core db init scripts failed")
	}
	defer neutreeCoreDeployManifestsTarFile.Close()

	if err := cleanKongPluginDirectory(renderedCoreWorkDir); err != nil {
		return errors.Wrap(err, "clean Kong plugin directory failed")
	}

	err = util.ExtractTar(neutreeCoreDeployManifestsTarFile, outputWorkDir)
	if err != nil {
		return errors.Wrap(err, "extract neutree core db init scripts failed")
	}

	coreWorkDir := filepath.Join(options.workDir, "neutree-core")
	pluginChecksums, err := kongPluginChecksums(filepath.Join(renderedCoreWorkDir, "gateway", "kong", "plugins"))
	if err != nil {
		return errors.Wrap(err, "calculate Kong plugin checksums failed")
	}

	// parseTemplate
	tempplateFiles := []string{
		filepath.Join(renderedCoreWorkDir, "docker-compose.yml"),
		filepath.Join(renderedCoreWorkDir, "vector", "vector.yml"),
	}

	jwtToken, err := storage.CreateServiceToken(options.jwtSecret)
	if err != nil {
		return errors.Wrap(err, "create jwt token failed")
	}

	// Fall back to the default when unset (e.g. options constructed directly
	// rather than via the CLI flag) so the compose never renders an empty
	// -retentionPeriod= argument.
	if options.victorialogsRetentionPeriod == "" {
		options.victorialogsRetentionPeriod = defaultVictoriaLogsRetentionPeriod
	}

	templateParams := map[string]string{
		"NeutreeCoreWorkDir":           coreWorkDir,
		"JwtSecret":                    options.jwtSecret,
		"DbPassword":                   options.dbPassword,
		"MetricsRemoteWriteURL":        options.metricsRemoteWriteURL,
		"GrafanaURL":                   options.grafanaURL,
		"VictoriaMetricsVersion":       componentversion.VictoriaMetrics,
		"VictoriaLogsRetentionPeriod":  options.victorialogsRetentionPeriod,
		"NeutreeVersion":               options.version,
		"JwtToken":                     *jwtToken,
		"VectorVersion":                componentversion.Vector,
		"KongVersion":                  componentversion.Kong,
		"KongPluginGatewayChecksum":    pluginChecksums["neutree-ai-gateway"],
		"KongPluginStatisticsChecksum": pluginChecksums["neutree-ai-statistics"],
		"KongPluginAccessChecksum":     pluginChecksums["neutree-ai-access"],
		"KongPluginQuotaChecksum":      pluginChecksums["neutree-ai-quota"],
		"NodeIP":                       options.nodeIP,
		"AdminPassword":                options.adminPassword,
	}

	err = util.BatchParseTemplateFiles(tempplateFiles, templateParams)
	if err != nil {
		return errors.Wrap(err, "parse template files failed")
	}

	composeFilePath := filepath.Join(renderedCoreWorkDir, "docker-compose.yml")
	err = replaceComposeImageRegistry(composeFilePath, options.mirrorRegistry, options.registryProject)

	if err != nil {
		return errors.Wrapf(err, "replace compose image registry failed, file path: %s", composeFilePath)
	}

	return nil
}

func cleanKongPluginDirectory(renderedCoreWorkDir string) error {
	return os.RemoveAll(filepath.Join(renderedCoreWorkDir, "gateway", "kong", "plugins"))
}
