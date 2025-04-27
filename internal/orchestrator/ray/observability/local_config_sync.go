package observability

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

type LocalConfigSync struct {
	metricsConfigPath string
}

func NewLocalConfigSync(configPath string) *LocalConfigSync {
	return &LocalConfigSync{
		metricsConfigPath: filepath.Join(configPath, "metrics"),
	}
}

func (s *LocalConfigSync) RemoveMetricsConfig(key string) error {
	metricsConfigFilePath := filepath.Join(s.metricsConfigPath, key+".json")
	if _, err := os.Stat(metricsConfigFilePath); err == nil {
		err := os.Remove(metricsConfigFilePath)
		if err != nil {
			return errors.Wrapf(err, "failed to remove metrics configs from file: %s", metricsConfigFilePath)
		}
	}

	return nil
}

func (s *LocalConfigSync) UpdateMetricsConfig(key string, configs []*v1.MetricsScrapeTargetsConfig) error {
	metricsConfigFilePath := filepath.Join(s.metricsConfigPath, key+".json")

	configContent, err := json.Marshal(configs)
	if err != nil {
		return errors.Wrapf(err, "failed to marshal metrics configs for key: %s", key)
	}

	err = os.WriteFile(metricsConfigFilePath, configContent, 0644) //nolint:gosec
	if err != nil {
		return errors.Wrapf(err, "failed to write metrics configs to file: %s", metricsConfigFilePath)
	}

	return nil
}
