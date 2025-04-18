package manager

import (
	"sync"

	"github.com/neutree-ai/neutree/internal/observability/config"
	"github.com/neutree-ai/neutree/internal/observability/monitoring"
)

type MetricsCollectConfigManager interface {
	RegisterMetricsMonitor(key string, sm monitoring.MetricsMonitor)
	UnregisterMetricsMonitor(key string)
	Sync() error
}

type metricsCollectConfigManager struct {
	metricsMonitorMap map[string]monitoring.MetricsMonitor
	configSyncer      config.ConfigSyncer
	lock              sync.Locker
}

func (m *metricsCollectConfigManager) RegisterMetricsMonitor(key string, sm monitoring.MetricsMonitor) {
	m.lock.Lock()
	defer m.lock.Unlock()

	m.metricsMonitorMap[key] = sm
}

func (m *metricsCollectConfigManager) UnregisterMetricsMonitor(key string) {
	m.lock.Lock()
	defer m.lock.Unlock()

	delete(m.metricsMonitorMap, key)
}

func (m *metricsCollectConfigManager) Sync() error {
	m.lock.Lock()
	defer m.lock.Unlock()

	return m.configSyncer.SyncMetricsCollectConfig(m.metricsMonitorMap)
}
