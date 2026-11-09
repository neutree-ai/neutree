package metrics

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"

	"github.com/Masterminds/sprig/v3"
	"github.com/VictoriaMetrics/VictoriaMetrics/lib/promrelabel"
	"gopkg.in/yaml.v3"

	"github.com/neutree-ai/neutree/internal/cluster/component/metrics/manifests"
)

var kubernetesVMAgentConfigTemplate = template.Must(
	template.New("kubernetes-vmagent-config").Funcs(sprig.TxtFuncMap()).Funcs(template.FuncMap{
		"toYaml": func(value interface{}) (string, error) {
			data, err := yaml.Marshal(value)
			return strings.TrimSuffix(string(data), "\n"), err
		},
	}).Parse(manifests.KubernetesVMAgentConfigTemplate),
)

func renderKubernetesVMAgentConfig(variables MetricsManifestVariables) (string, error) {
	variables = withBuiltinScrapeTargets(variables)
	var output bytes.Buffer

	if err := kubernetesVMAgentConfigTemplate.Execute(&output, variables); err != nil {
		return "", err
	}

	if err := validateRelabelConfigs(output.Bytes()); err != nil {
		return "", err
	}

	return output.String(), nil
}

func validateRelabelConfigs(data []byte) error {
	var config struct {
		ScrapeConfigs []struct {
			JobName        string    `yaml:"job_name"`
			RelabelConfigs yaml.Node `yaml:"relabel_configs"`
		} `yaml:"scrape_configs"`
	}

	if err := yaml.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("parse vmagent scrape config: %w", err)
	}

	jobNames := make(map[string]struct{}, len(config.ScrapeConfigs))

	for _, job := range config.ScrapeConfigs {
		if job.JobName != "" {
			if _, exists := jobNames[job.JobName]; exists {
				return fmt.Errorf("duplicate scrape job name %q", job.JobName)
			}

			jobNames[job.JobName] = struct{}{}
		}

		rules, err := yaml.Marshal(&job.RelabelConfigs)
		if err != nil {
			return fmt.Errorf("encode relabel configs for job %q: %w", job.JobName, err)
		}

		if _, err := promrelabel.ParseRelabelConfigsData(rules); err != nil {
			return fmt.Errorf("parse relabel configs for job %q: %w", job.JobName, err)
		}
	}

	return nil
}
