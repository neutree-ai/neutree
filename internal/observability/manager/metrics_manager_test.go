package manager

import (
	"sync"
	"testing"

	configmocks "github.com/neutree-ai/neutree/internal/observability/config/mocks"
	"github.com/neutree-ai/neutree/internal/observability/monitoring"
	monitoringmocks "github.com/neutree-ai/neutree/internal/observability/monitoring/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestMetricsCollectConfigManager(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(*configmocks.MockConfigSyncer, *monitoringmocks.MockMetricsMonitor)
		testFunc    func(MetricsCollectConfigManager, *monitoringmocks.MockMetricsMonitor)
		expectError bool
	}{
		{
			name: "RegisterMetricsMonitor should add monitor to map",
			setup: func(syncer *configmocks.MockConfigSyncer, monitor *monitoringmocks.MockMetricsMonitor) {
				// No expectations needed for register
			},
			testFunc: func(m MetricsCollectConfigManager, monitor *monitoringmocks.MockMetricsMonitor) {
				m.RegisterMetricsMonitor("test-key", monitor)
			},
		},
		{
			name: "UnregisterMetricsMonitor should remove monitor from map",
			setup: func(syncer *configmocks.MockConfigSyncer, monitor *monitoringmocks.MockMetricsMonitor) {
				// No expectations needed for unregister
			},
			testFunc: func(m MetricsCollectConfigManager, monitor *monitoringmocks.MockMetricsMonitor) {
				m.RegisterMetricsMonitor("test-key", monitor)
				m.UnregisterMetricsMonitor("test-key")
			},
		},
		{
			name: "Sync should call config syncer with current monitors",
			setup: func(syncer *configmocks.MockConfigSyncer, monitor *monitoringmocks.MockMetricsMonitor) {
				syncer.On("SyncMetricsCollectConfig", mock.AnythingOfType("map[string]monitoring.MetricsMonitor")).Return(nil)
			},
			testFunc: func(m MetricsCollectConfigManager, monitor *monitoringmocks.MockMetricsMonitor) {
				m.RegisterMetricsMonitor("test-key", monitor)
				err := m.Sync()
				assert.NoError(t, err)
			},
		},
		{
			name: "Sync should return error when config syncer fails",
			setup: func(syncer *configmocks.MockConfigSyncer, monitor *monitoringmocks.MockMetricsMonitor) {
				syncer.On("SyncMetricsCollectConfig", mock.Anything).Return(assert.AnError)
			},
			testFunc: func(m MetricsCollectConfigManager, monitor *monitoringmocks.MockMetricsMonitor) {
				m.RegisterMetricsMonitor("test-key", monitor)
				err := m.Sync()
				assert.Error(t, err)
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup mocks
			mockSyncer := new(configmocks.MockConfigSyncer)
			mockMonitor := new(monitoringmocks.MockMetricsMonitor)
			tt.setup(mockSyncer, mockMonitor)

			// Create manager instance
			manager := &metricsCollectConfigManager{
				metricsMonitorMap: make(map[string]monitoring.MetricsMonitor),
				configSyncer:      mockSyncer,
				lock:              &sync.Mutex{},
			}

			// Execute test
			tt.testFunc(manager, mockMonitor)

			// Verify mock expectations
			mockSyncer.AssertExpectations(t)
			mockMonitor.AssertExpectations(t)
		})
	}
}
