package launch

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/cmd/launch/manifests"
	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/constants"
	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/util"
	"github.com/neutree-ai/neutree/pkg/command"
	"github.com/neutree-ai/neutree/pkg/helmclient"
	"github.com/neutree-ai/neutree/pkg/storage"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
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
	// support compose and helm
	if options.deployMethod == "helm" || options.deployMethod == "kubernetes" {
		return installNeutreeCoreByHelm(exector, options)
	}

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

	// pull images referenced in compose using Docker client SDK before starting
	composeFile := filepath.Join(options.workDir, "neutree-core", "docker-compose.yml")
	_, errPull := pullImagesFromCompose(context.Background(), composeFile)
	if errPull != nil {
		return errors.Wrap(errPull, "pull compose images failed")
	}
	if err != nil {
		return errors.Wrap(err, "pull compose images failed")
	}

	// Bring up compose using SDK runner
	if composeSDKRunner == nil {
		// fallback to docker CLI if compose SDK runner is not set
		output, err := exector.Execute(context.Background(), "docker",
			[]string{"compose", "-p", "neutree-core", "-f", composeFile, "up", "-d"})
		if err != nil {
			return errors.Wrapf(err, "error when executing docker compose up, failed msg %s", string(output))
		}
	} else {
		if err := composeSDKRunner.Up(context.Background(), composeFile, "neutree-core"); err != nil {
			return errors.Wrap(err, "compose up failed")
		}
	}

	return nil
}

func prepareNeutreeCoreDeployConfig(options neutreeCoreInstallOptions) error {
	// If an offline package is provided, extract it to the workDir.
	if options.offlinePackage != "" {
		f, err := os.Open(options.offlinePackage)
		if err != nil {
			return err
		}
		defer f.Close()

		var reader io.Reader = f
		if strings.HasSuffix(options.offlinePackage, ".gz") || strings.HasSuffix(options.offlinePackage, ".tgz") {
			gz, err := gzip.NewReader(f)
			if err != nil {
				return err
			}
			defer gz.Close()
			reader = gz
		}

		if err := util.ExtractTar(reader, options.workDir); err != nil {
			return errors.Wrap(err, "extract offline package failed")
		}

		// continue to replace image registries and template parsing below
	} else {
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

	}

	coreWorkDir := filepath.Join(options.workDir, "neutree-core")

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
		"NeutreeVersion":         options.version,
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

// install using Helm chart
func installNeutreeCoreByHelm(exector command.Executor, options neutreeCoreInstallOptions) error {
	// Extract helm chart if offlinePackage is present
	chartPath := filepath.Join("deploy", "chart", "neutree")
	if options.offlinePackage != "" {
		f, err := os.Open(options.offlinePackage)
		if err != nil {
			return err
		}
		defer f.Close()

		var reader io.Reader = f
		if strings.HasSuffix(options.offlinePackage, ".gz") || strings.HasSuffix(options.offlinePackage, ".tgz") {
			gz, err := gzip.NewReader(f)
			if err != nil {
				return err
			}
			defer gz.Close()
			reader = gz
		}

		if err := util.ExtractTar(reader, options.workDir); err != nil {
			return errors.Wrap(err, "extract helm chart failed")
		}

		// chart is expected to be extracted under workdir/neutree
		chartPath = filepath.Join(options.workDir, "neutree")
	}

	jwtToken, err := storage.CreateServiceToken(options.jwtSecret)
	if err != nil {
		return errors.Wrap(err, "create jwt token failed")
	}

	// Always use Helm SDK for install/upgrade
	var hc helmclient.HelmClient = helmclient.NewSDKClient()

	if err := installNeutreeCoreByHelmWithClient(hc, chartPath, options, jwtToken); err != nil {
		return err
	}

	return nil
}

func installNeutreeCoreByHelmWithClient(hc helmclient.HelmClient, chartPath string, options neutreeCoreInstallOptions, jwtToken *string) error {
	setArgs := []string{fmt.Sprintf("jwtSecret=%s", *jwtToken)}
	if options.metricsRemoteWriteURL != "" {
		setArgs = append(setArgs, fmt.Sprintf("metrics.remoteWriteUrl=%s", options.metricsRemoteWriteURL))
	}
	if options.grafanaURL != "" {
		setArgs = append(setArgs, fmt.Sprintf("system.grafana.url=%s", options.grafanaURL))
	}
	if options.mirrorRegistry != "" {
		setArgs = append(setArgs, fmt.Sprintf("grafana.image.registry=%s", options.mirrorRegistry))
		setArgs = append(setArgs, fmt.Sprintf("victoria-metrics-cluster.global.image.registry=%s", options.mirrorRegistry))
		setArgs = append(setArgs, fmt.Sprintf("global.imageRegistry=%s", options.mirrorRegistry))
	}

	values := map[string]interface{}{}
	values["jwtSecret"] = *jwtToken
	if options.metricsRemoteWriteURL != "" {
		values["metrics"] = map[string]interface{}{"remoteWriteUrl": options.metricsRemoteWriteURL}
	}
	if options.grafanaURL != "" {
		values["system"] = map[string]interface{}{"grafana": map[string]interface{}{"url": options.grafanaURL}}
	}
	if options.mirrorRegistry != "" {
		values["global"] = map[string]interface{}{"imageRegistry": options.mirrorRegistry}
		values["grafana"] = map[string]interface{}{"image": map[string]interface{}{"registry": options.mirrorRegistry}}
		values["victoria-metrics-cluster"] = map[string]interface{}{"global": map[string]interface{}{"image": map[string]interface{}{"registry": options.mirrorRegistry}}}
	}

	_, err := hc.UpgradeInstall(context.Background(), "neutree", chartPath, "neutree", values, setArgs)
	if err != nil {
		return errors.Wrap(err, "helm install failed")
	}

	return nil
}
