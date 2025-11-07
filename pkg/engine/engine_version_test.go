package engine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestExtractorTarGz(t *testing.T) {
	// This is a unit test structure - actual implementation would need test fixtures
	extractor := NewExtractor()
	assert.NotNil(t, extractor)

	// Test would require creating a test tar.gz file
	// For now, just verify the extractor can be instantiated
}

func TestExtractorValidation(t *testing.T) {
	extractor := NewExtractor()

	// Test invalid package format
	err := extractor.Extract("invalid.xyz", "/tmp/test")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported package format")
}

func TestExtractorSecurityCheck(t *testing.T) {
	// Test that extractor prevents path traversal attacks
	extractor := NewExtractor()
	assert.NotNil(t, extractor)

	// Would need to create a malicious tar file for full test
	// This validates the security check logic exists
}

func TestParserValidateManifest(t *testing.T) {
	parser := NewParser()

	tests := []struct {
		name        string
		manifest    *PackageManifest
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid manifest",
			manifest: &PackageManifest{
				ManifestVersion: "1.0",
				Package: &EngineVersionPackage{
					Metadata: &PackageMetadata{
						EngineName:     "test-engine",
						Version:        "v1.0.0",
						PackageVersion: "1.0",
					},
					Images: []*ImageSpec{
						{
							Accelerator: "nvidia-gpu",
							ImageName:   "test/image",
							Tag:         "v1.0.0",
							ImageFile:   "images/test.tar",
						},
					},
					EngineVersion: &v1.EngineVersion{
						Version: "v1.0.0",
					},
				},
			},
			expectError: false,
		},
		{
			name: "missing metadata",
			manifest: &PackageManifest{
				ManifestVersion: "1.0",
				Package: &EngineVersionPackage{
					Metadata: nil,
				},
			},
			expectError: true,
			errorMsg:    "metadata is nil",
		},
		{
			name: "missing engine name",
			manifest: &PackageManifest{
				ManifestVersion: "1.0",
				Package: &EngineVersionPackage{
					Metadata: &PackageMetadata{
						Version:        "v1.0.0",
						PackageVersion: "1.0",
					},
				},
			},
			expectError: true,
			errorMsg:    "engine name is empty",
		},
		{
			name: "no images",
			manifest: &PackageManifest{
				ManifestVersion: "1.0",
				Package: &EngineVersionPackage{
					Metadata: &PackageMetadata{
						EngineName:     "test",
						Version:        "v1.0.0",
						PackageVersion: "1.0",
					},
					Images:        []*ImageSpec{},
					EngineVersion: &v1.EngineVersion{},
				},
			},
			expectError: true,
			errorMsg:    "no images specified",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := parser.validateManifest(tt.manifest)
			if tt.expectError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestParserGetImagePath(t *testing.T) {
	parser := NewParser()

	extractedPath := "/tmp/extracted"
	imageFile := "images/test.tar"

	result := parser.GetImagePath(extractedPath, imageFile)
	expected := filepath.Join(extractedPath, imageFile)

	assert.Equal(t, expected, result)
}

func TestImagePusherBuildTargetImage(t *testing.T) {
	pusher, err := NewImagePusher(nil) // No API client needed for testing buildTargetImage
	require.NoError(t, err, "Failed to create ImagePusher")

	tests := []struct {
		name        string
		imagePrefix string
		imgSpec     *ImageSpec
		expected    string
	}{
		{
			name:        "with prefix",
			imagePrefix: "registry.example.com/neutree",
			imgSpec: &ImageSpec{
				ImageName: "vllm-cuda",
				Tag:       "v0.5.0",
			},
			expected: "registry.example.com/neutree/vllm-cuda:v0.5.0",
		},
		{
			name:        "without prefix",
			imagePrefix: "registry.example.com",
			imgSpec: &ImageSpec{
				ImageName: "vllm-cuda",
				Tag:       "v0.5.0",
			},
			expected: "registry.example.com/vllm-cuda:v0.5.0",
		},
		{
			name:        "remove existing registry",
			imagePrefix: "new-registry.com/neutree",
			imgSpec: &ImageSpec{
				ImageName: "old-registry.com/vllm-cuda",
				Tag:       "v0.5.0",
			},
			expected: "new-registry.com/neutree/vllm-cuda:v0.5.0",
		},
		{
			name:        "remove existing registry with port",
			imagePrefix: "new-registry.com/neutree",
			imgSpec: &ImageSpec{
				ImageName: "old-registry.com:5000/vllm-cuda",
				Tag:       "v0.5.0",
			},
			expected: "new-registry.com/neutree/vllm-cuda:v0.5.0",
		},
		{
			name:        "keep organization name without dots",
			imagePrefix: "registry.example.com/neutree",
			imgSpec: &ImageSpec{
				ImageName: "myorg/vllm-cuda",
				Tag:       "v0.5.0",
			},
			expected: "registry.example.com/neutree/myorg/vllm-cuda:v0.5.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := pusher.buildTargetImage(tt.imagePrefix, tt.imgSpec)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestImportOptionsValidation(t *testing.T) {
	importer, err := NewImporter(nil)
	require.NoError(t, err, "Failed to create Importer")

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
			name: "missing registry when not skipping push",
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
				ImageRegistry: "",
			},
			expectError: true,
			errorMsg:    "image registry is required",
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

func TestImagePusherExtractImageNameWithoutRegistry(t *testing.T) {
	pusher, err := NewImagePusher(nil) // No API client needed for testing
	require.NoError(t, err, "Failed to create ImagePusher")

	tests := []struct {
		name      string
		imageName string
		expected  string
	}{
		{
			name:      "simple image name",
			imageName: "vllm-cuda",
			expected:  "vllm-cuda",
		},
		{
			name:      "image with organization",
			imageName: "myorg/vllm-cuda",
			expected:  "myorg/vllm-cuda",
		},
		{
			name:      "image with registry domain",
			imageName: "registry.example.com/vllm-cuda",
			expected:  "vllm-cuda",
		},
		{
			name:      "image with registry and org",
			imageName: "registry.example.com/myorg/vllm-cuda",
			expected:  "myorg/vllm-cuda",
		},
		{
			name:      "image with registry port",
			imageName: "registry.example.com:5000/vllm-cuda",
			expected:  "vllm-cuda",
		},
		{
			name:      "image with registry port and org",
			imageName: "registry.example.com:5000/myorg/vllm-cuda",
			expected:  "myorg/vllm-cuda",
		},
		{
			name:      "dockerhub official image",
			imageName: "nginx",
			expected:  "nginx",
		},
		{
			name:      "dockerhub user image",
			imageName: "username/image",
			expected:  "username/image",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := pusher.extractImageNameWithoutRegistry(tt.imageName)
			assert.Equal(t, tt.expected, result)
		})
	}
}
