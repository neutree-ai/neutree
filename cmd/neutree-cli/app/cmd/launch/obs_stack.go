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
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
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
		if options.deployMethod == "helm" || options.deployMethod == "kubernetes" {
			return installObsStackByHelm(exector, options)
		}
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

	// pull images referenced in compose using Docker client SDK before starting
	composeFile := filepath.Join(options.workDir, "obs-stack", "docker-compose.yml")
	_, errPull := pullImagesFromCompose(context.Background(), composeFile)
	if errPull != nil {
		return errors.Wrap(errPull, "pull compose images failed")
	}

	// Bring up compose via SDK runner, fallback to docker CLI when nil
	if composeSDKRunner == nil {
		output, err := exector.Execute(context.Background(), "docker",
			[]string{"compose", "-p", "obs-stack", "-f", composeFile, "up", "-d"})
		if err != nil {
			return errors.Wrapf(err, "error when executing docker compose up, failed msg %s", string(output))
		}
	} else {
		if err := composeSDKRunner.Up(context.Background(), composeFile, "obs-stack"); err != nil {
			return errors.Wrap(err, "compose up failed")
		}
	}

	return nil
}

func prepareObsStackDeployConfig(options *obsStackInstallOptions) error {
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
		// continue to template parsing and registry replacement below
	} else {
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

	}

	var err error

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

// Install observability stack using Helm chart
func installObsStackByHelm(exector command.Executor, options obsStackInstallOptions) error {
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

		chartPath = filepath.Join(options.workDir, "neutree")
	}

	// create helm client
	// Always use Helm SDK for installs/upgrades. CLI fallbacks were removed.
	var hc helmclient.HelmClient = helmclient.NewSDKClient()

	setArgs := []string{}
	setArgs = append(setArgs, "grafana.enabled=true")
	setArgs = append(setArgs, "victoria-metrics-cluster.enabled=true")

	if options.mirrorRegistry != "" {
		setArgs = append(setArgs, fmt.Sprintf("grafana.image.registry=%s", options.mirrorRegistry))
		setArgs = append(setArgs, fmt.Sprintf("victoria-metrics-cluster.global.image.registry=%s", options.mirrorRegistry))
		setArgs = append(setArgs, fmt.Sprintf("global.imageRegistry=%s", options.mirrorRegistry))
	}

	values := map[string]interface{}{}
	values["grafana"] = map[string]interface{}{"enabled": true}
	values["victoria-metrics-cluster"] = map[string]interface{}{"enabled": true}
	if options.mirrorRegistry != "" {
		values["global"] = map[string]interface{}{"imageRegistry": options.mirrorRegistry}
		values["grafana"] = map[string]interface{}{"image": map[string]interface{}{"registry": options.mirrorRegistry}}
		values["victoria-metrics-cluster"] = map[string]interface{}{"global": map[string]interface{}{"image": map[string]interface{}{"registry": options.mirrorRegistry}}}
	}

	if err := installObsStackByHelmWithClient(hc, chartPath, options, values, setArgs); err != nil {
		return errors.Wrap(err, "helm install obs-stack failed")
	}
	return nil
}

func installObsStackByHelmWithClient(hc helmclient.HelmClient, chartPath string, options obsStackInstallOptions, values map[string]interface{}, setArgs []string) error {
	_, err := hc.UpgradeInstall(context.Background(), "neutree", chartPath, "neutree", values, setArgs)
	if err != nil {
		return err
	}
	return nil
}
