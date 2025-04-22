package config

import "github.com/neutree-ai/neutree/internal/observability/monitoring"

type ConfigSyncer interface {
	SyncMetricsCollectConfig(metricsMonitorMap map[string]monitoring.MetricsMonitor) error
}
