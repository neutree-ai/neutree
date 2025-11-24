package packageimport

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestImagePusherBuildTargetImage(t *testing.T) {
	pusher, err := NewImagePusher() // No API client needed for testing buildTargetImage
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

func TestImagePusherExtractImageNameWithoutRegistry(t *testing.T) {
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
			result := extractImageNameWithoutRegistry(tt.imageName)
			assert.Equal(t, tt.expected, result)
		})
	}
}
