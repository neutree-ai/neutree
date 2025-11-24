package packageimport

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractorValidation(t *testing.T) {
	extractor := NewExtractor()

	// Test invalid package format
	err := extractor.Extract("invalid.xyz", "/tmp/test")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported package format")
}

func TestImportOptionsValidation(t *testing.T) {
	importer := NewImporter(nil)

	tests := []struct {
		name        string
		opts        *ImportOptions
		setupFunc   func() string // Returns temp file path
		cleanupFunc func(string)
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid options with skip image push",
			setupFunc: func() string {
				tmpFile, _ := os.CreateTemp("", "test-*.tar.gz")
				tmpFile.Close()
				return tmpFile.Name()
			},
			cleanupFunc: func(path string) {
				os.Remove(path)
			},
			opts: &ImportOptions{
				PackagePath:   "", // Will be set by setupFunc
				SkipImagePush: true,
			},
			expectError: false,
		},
		{
			name: "missing package path",
			opts: &ImportOptions{
				PackagePath: "",
			},
			expectError: true,
			errorMsg:    "package path is required",
		},
		{
			name: "package file not found",
			opts: &ImportOptions{
				PackagePath: "/nonexistent/package.tar.gz",
			},
			expectError: true,
			errorMsg:    "package file not found",
		},
		{
			name: "with registry when not skipping push",
			setupFunc: func() string {
				tmpFile, _ := os.CreateTemp("", "test-*.tar.gz")
				tmpFile.Close()
				return tmpFile.Name()
			},
			cleanupFunc: func(path string) {
				os.Remove(path)
			},
			opts: &ImportOptions{
				PackagePath:   "", // Will be set by setupFunc
				SkipImagePush: false,
				ImageRegistry: "registry.example.com",
				Workspace:     "default",
			},
			expectError: false,
		},
		{
			name: "with mirror registry when not skipping push",
			setupFunc: func() string {
				tmpFile, _ := os.CreateTemp("", "test-*.tar.gz")
				tmpFile.Close()
				return tmpFile.Name()
			},
			cleanupFunc: func(path string) {
				os.Remove(path)
			},
			opts: &ImportOptions{
				PackagePath:      "", // Will be set by setupFunc
				SkipImagePush:    false,
				MirrorRegistry:   "registry.mirror.com",
				RegistryUser:     "user",
				RegistryPassword: "pass",
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setupFunc != nil {
				tmpPath := tt.setupFunc()
				tt.opts.PackagePath = tmpPath
				if tt.cleanupFunc != nil {
					defer tt.cleanupFunc(tmpPath)
				}
			}

			err := importer.validateOptions(tt.opts)
			if tt.expectError {
				require.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
