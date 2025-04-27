package observability

import (
	"os"
	"path/filepath"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/assert"
)

func TestRemoveConfig(t *testing.T) {
	tempDir := os.TempDir()
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
			err := sync.RemoveMetricsConfig(tt.key)

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
	tempDir := os.TempDir()
	configPath := filepath.Join(tempDir, "neutree_test_config_update")
	defer os.RemoveAll(configPath)

	tests := []struct {
		name        string
		key         string
		configs     []*v1.MetricsScrapeTargetsConfig
		expectError bool
	}{
		{
			name: "successful update",
			key:  "metrics1",
			configs: []*v1.MetricsScrapeTargetsConfig{
				{Targets: []string{"localhost:9090"}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.RemoveAll(configPath)
			os.MkdirAll(filepath.Join(configPath, "metrics"), 0755)

			sync := NewLocalConfigSync(configPath)
			err := sync.UpdateMetricsConfig(tt.key, tt.configs)

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
