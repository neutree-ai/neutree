package util

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestBuildClusterImageRef(t *testing.T) {
	tests := []struct {
		name        string
		imagePrefix string
		version     string
		imageSuffix string
		expected    string
	}{
		{
			name:        "default nvidia (no suffix)",
			imagePrefix: "registry.io/neutree",
			version:     "v1.0.0",
			imageSuffix: "",
			expected:    "registry.io/neutree/neutree/neutree-serve:v1.0.0",
		},
		{
			name:        "amd rocm suffix",
			imagePrefix: "registry.io/neutree",
			version:     "v1.0.0",
			imageSuffix: "rocm",
			expected:    "registry.io/neutree/neutree/neutree-serve:v1.0.0-rocm",
		},
		{
			name:        "rc version with suffix",
			imagePrefix: "registry.io/neutree",
			version:     "v1.0.1-rc.1",
			imageSuffix: "rocm",
			expected:    "registry.io/neutree/neutree/neutree-serve:v1.0.1-rc.1-rocm",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildClusterImageRef(tt.imagePrefix, tt.version, tt.imageSuffix)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestBuildEngineImageRef(t *testing.T) {
	tests := []struct {
		name        string
		imagePrefix string
		engineImage *v1.EngineImage
		expected    string
	}{
		{
			name:        "normal engine image",
			imagePrefix: "registry.io/neutree",
			engineImage: &v1.EngineImage{ImageName: "neutree/vllm", Tag: "v0.11.2"},
			expected:    "registry.io/neutree/neutree/vllm:v0.11.2",
		},
		{
			name:        "nil engine image",
			imagePrefix: "registry.io/neutree",
			engineImage: nil,
			expected:    "",
		},
		{
			name:        "empty image name",
			imagePrefix: "registry.io/neutree",
			engineImage: &v1.EngineImage{ImageName: "", Tag: "v0.11.2"},
			expected:    "",
		},
		{
			name:        "no prefix",
			imagePrefix: "",
			engineImage: &v1.EngineImage{ImageName: "neutree/vllm", Tag: "v0.11.2"},
			expected:    "neutree/vllm:v0.11.2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildEngineImageRef(tt.imagePrefix, tt.engineImage)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestResolveEngineImage(t *testing.T) {
	ev := &v1.EngineVersion{
		Version: "v0.11.2",
		Images: map[string]*v1.EngineImage{
			"nvidia_gpu": {ImageName: "neutree/vllm", Tag: "v0.11.2"},
			"amd_gpu":    {ImageName: "neutree/vllm-rocm", Tag: "v0.11.2"},
		},
	}

	t.Run("nvidia", func(t *testing.T) {
		ref, err := ResolveEngineImage(ev, "nvidia_gpu", "registry.io/neutree")
		require.NoError(t, err)
		assert.Equal(t, "registry.io/neutree/neutree/vllm:v0.11.2", ref)
	})

	t.Run("amd", func(t *testing.T) {
		ref, err := ResolveEngineImage(ev, "amd_gpu", "registry.io/neutree")
		require.NoError(t, err)
		assert.Equal(t, "registry.io/neutree/neutree/vllm-rocm:v0.11.2", ref)
	})

	t.Run("empty accelerator defaults to cpu", func(t *testing.T) {
		evWithCPU := &v1.EngineVersion{
			Version: "v0.11.2",
			Images: map[string]*v1.EngineImage{
				"cpu": {ImageName: "neutree/vllm-cpu", Tag: "v0.11.2"},
			},
		}
		ref, err := ResolveEngineImage(evWithCPU, "", "registry.io/neutree")
		require.NoError(t, err)
		assert.Equal(t, "registry.io/neutree/neutree/vllm-cpu:v0.11.2", ref)
	})

	t.Run("missing accelerator", func(t *testing.T) {
		ref, err := ResolveEngineImage(ev, "cpu", "registry.io/neutree")
		require.NoError(t, err)
		assert.Empty(t, ref)
	})

	t.Run("nil engine version", func(t *testing.T) {
		_, err := ResolveEngineImage(nil, "nvidia_gpu", "registry.io/neutree")
		require.Error(t, err)
	})
}
