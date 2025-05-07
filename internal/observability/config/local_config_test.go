package config

import (
	"os"
	"path/filepath"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/observability/monitoring"
	"github.com/neutree-ai/neutree/internal/observability/monitoring/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLocalConfigSync(t *testing.T) {
	// Setup temp directory
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "neutree_test_config")
	defer os.RemoveAll(configPath)

	// Create test cases
	tests := []struct {
		name              string
		metricsMonitorMap map[string]monitoring.MetricsMonitor
		setupFiles        []string // files to create before test
		expectedFiles     []string // files expected after test
		expectError       bool
	}{
		{
			name: "successful sync with new configs",
			metricsMonitorMap: map[string]monitoring.MetricsMonitor{
				"metrics1": &mocks.MockMetricsMonitor{},
				"metrics2": &mocks.MockMetricsMonitor{},
			},
			expectedFiles: []string{"metrics1.json", "metrics2.json"},
		},
		{
			name: "remove outdated configs",
			metricsMonitorMap: map[string]monitoring.MetricsMonitor{
				"metrics1": &mocks.MockMetricsMonitor{},
			},
			setupFiles:    []string{"metrics1.json", "old_metrics.json"},
			expectedFiles: []string{"metrics1.json"},
		},
		{
			name: "error when reading directory fails",
			metricsMonitorMap: map[string]monitoring.MetricsMonitor{
				"metrics1": &mocks.MockMetricsMonitor{},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset test directory
			os.RemoveAll(configPath)
			os.MkdirAll(filepath.Join(configPath, "metrics"), 0755)

			// Setup initial files if needed
			for _, file := range tt.setupFiles {
				fp := filepath.Join(configPath, "metrics", file)
				os.WriteFile(fp, []byte("{}"), 0644)
			}

			// Mock behavior for GetMetricsScrapeTargetsConfig
			for _, sm := range tt.metricsMonitorMap {
				mockSM := sm.(*mocks.MockMetricsMonitor)
				mockSM.On("GetMetricsScrapeTargetsConfig").Return([]v1.MetricsScrapeTargetsConfig{}, nil)
			}

			// Create config sync instance
			sync := NewLocalConfigSync(configPath)

			// Special case: simulate directory read error
			if tt.expectError && len(tt.metricsMonitorMap) > 0 {
				os.RemoveAll(filepath.Join(configPath, "metrics"))
				os.WriteFile(filepath.Join(configPath, "metrics"), []byte(""), 0644) // make it a file to cause error
			}

			// Execute test
			err := sync.SyncMetricsCollectConfig(tt.metricsMonitorMap)

			// Verify results
			if tt.expectError {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)

			// Verify expected files exist
			files, err := os.ReadDir(filepath.Join(configPath, "metrics"))
			require.NoError(t, err)

			var actualFiles []string
			for _, file := range files {
				actualFiles = append(actualFiles, file.Name())
			}

			assert.ElementsMatch(t, tt.expectedFiles, actualFiles)
		})
	}
}

func TestRemoveConfig(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "neutree_test_config_remove")
	defer os.RemoveAll(configPath)

	tests := []struct {
		name        string
		setupFile   string
		key         string
		expectError bool
	}{
		{
			name:      "successful remove",
			setupFile: "metrics1.json",
			key:       "metrics1",
		},
		{
			name:        "file not exists",
			key:         "nonexistent",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.RemoveAll(configPath)
			os.MkdirAll(filepath.Join(configPath, "metrics"), 0755)

			if tt.setupFile != "" {
				fp := filepath.Join(configPath, "metrics", tt.setupFile)
				os.WriteFile(fp, []byte("{}"), 0644)
			}

			sync := NewLocalConfigSync(configPath)
			err := sync.removeMetricsConfig(tt.key)

			if tt.expectError {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			_, err = os.Stat(filepath.Join(configPath, "metrics", tt.key+".json"))
			assert.True(t, os.IsNotExist(err))
		})
	}
}

func TestUpdateConfig(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "neutree_test_config_update")
	defer os.RemoveAll(configPath)

	tests := []struct {
		name        string
		key         string
		configs     []v1.MetricsScrapeTargetsConfig
		expectError bool
	}{
		{
			name: "successful update",
			key:  "metrics1",
			configs: []v1.MetricsScrapeTargetsConfig{
				{Targets: []string{"localhost:9090"}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.RemoveAll(configPath)
			os.MkdirAll(filepath.Join(configPath, "metrics"), 0755)

			sync := NewLocalConfigSync(configPath)
			err := sync.updateMetricsConfig(tt.key, tt.configs)

			if tt.expectError {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			_, err = os.Stat(filepath.Join(configPath, "metrics", tt.key+".json"))
			assert.NoError(t, err)
		})
	}
}
