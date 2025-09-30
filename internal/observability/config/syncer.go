package config

import v1 "github.com/neutree-ai/neutree/api/v1"

type ConfigSyncer interface {
	SyncMetricsCollectConfig(scrapeTargets map[string][]v1.MetricsScrapeTargetsConfig) error
}
