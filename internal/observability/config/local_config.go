package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/observability/monitoring"
)

type LocalConfigSync struct {
	metricsConfigPath string
}

func NewLocalConfigSync(configPath string) *LocalConfigSync {
	return &LocalConfigSync{
		metricsConfigPath: filepath.Join(configPath, "metrics"),
	}
}

func (s *LocalConfigSync) SyncMetricsCollectConfig(metricsMonitorMap map[string]monitoring.MetricsMonitor) error {
	// remove useless scrape configs from local metrics scrape config path
	entries, err := os.ReadDir(s.metricsConfigPath)
	if err != nil {
		return errors.Wrapf(err, "failed to read metrics config path: %s", s.metricsConfigPath)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		key := strings.Split(entry.Name(), ".")[0]

		_, ok := metricsMonitorMap[key]
		if ok {
			continue
		}

		err = s.removeMetricsConfig(key)
		if err != nil {
			return errors.Wrapf(err, "failed to remove metrics configs for key: %s", key)
		}
	}

	// write current scrape configs to local metrics scrape config path
	for key, monitor := range metricsMonitorMap {
		metricsConfigs, err := monitor.GetMetricsScrapeTargetsConfig()
		if err != nil {
			return errors.Wrapf(err, "failed to get metrics configs for key: %s", key)
		}

		err = s.updateMetricsConfig(key, metricsConfigs)
		if err != nil {
			return errors.Wrapf(err, "failed to update metrics configs for key: %s", key)
		}
	}

	return nil
}

func (s *LocalConfigSync) removeMetricsConfig(key string) error {
	metricsConfigFilePath := filepath.Join(s.metricsConfigPath, key+".json")
	err := os.Remove(metricsConfigFilePath)

	if err != nil {
		return errors.Wrapf(err, "failed to remove metrics configs from file: %s", metricsConfigFilePath)
	}

	return nil
}

func (s *LocalConfigSync) updateMetricsConfig(key string, configs []v1.MetricsScrapeTargetsConfig) error {
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
