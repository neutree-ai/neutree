package manager

import (
	"sync"
	"testing"

	configmocks "github.com/neutree-ai/neutree/internal/observability/config/mocks"
	"github.com/neutree-ai/neutree/internal/observability/monitoring"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestNewObsCollectConfigManager(t *testing.T) {
	tests := []struct {
		name        string
		deployType  string
		configPath  string
		expectError bool
		expectNil   bool
	}{
		{
			name:        "success with local deploy type",
			configPath:  "/tmp/config",
			expectError: false,
			expectNil:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager, err := NewObsCollectConfigManager(ObsCollectConfigOptions{
				LocalCollectConfigPath: tt.configPath,
			})

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			if tt.expectNil {
				assert.Nil(t, manager)
			} else {
				assert.NotNil(t, manager)
			}
		})
	}
}

func TestObsCollectConfigManagerMethods(t *testing.T) {
	// Setup mocks
	mockSyncer := new(configmocks.MockConfigSyncer)

	// Create test manager
	manager := &obsCollectConfigManager{
		metricsCollectConfigManager: &metricsCollectConfigManager{
			configSyncer:      mockSyncer,
			metricsMonitorMap: make(map[string]monitoring.MetricsMonitor),
			lock:              &sync.Mutex{},
		},
		firstSync: true,
	}

	t.Run("GetMetricsCollectConfigManager returns non-nil", func(t *testing.T) {
		mccm := manager.GetMetricsCollectConfigManager()
		assert.NotNil(t, mccm)
	})

	t.Run("sync calls metrics manager sync", func(t *testing.T) {
		mockSyncer.On("SyncMetricsCollectConfig", mock.Anything).Once().Return(nil)

		err := manager.sync()
		assert.NoError(t, err)
		mockSyncer.AssertExpectations(t)
	})

	t.Run("sync handles error from metrics manager", func(t *testing.T) {
		mockSyncer.On("SyncMetricsCollectConfig", mock.Anything).Once().Return(assert.AnError)

		err := manager.sync()
		assert.Error(t, err)
		mockSyncer.AssertExpectations(t)
	})
}
