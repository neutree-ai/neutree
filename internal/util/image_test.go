package util

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestBuildProfileImageRef(t *testing.T) {
	tests := []struct {
		name        string
		imagePrefix string
		component   string
		ref         v1.ImageRef
		expected    string
		expectError string
	}{
		{
			name:        "rewrites source registry into the configured registry",
			imagePrefix: "registry.example.com/neutree",
			component:   "router",
			ref:         v1.ImageRef{Image: "quay.io/neutree/router", Tag: "v1.2.0"},
			expected:    "registry.example.com/neutree/neutree/router:v1.2.0",
		},
		{
			name:        "keeps docker hub images unchanged",
			imagePrefix: "docker.io/neutree",
			component:   "node exporter",
			ref:         v1.ImageRef{Image: "quay.io/prometheus/node-exporter", Tag: "v1.8.2"},
			expected:    "quay.io/prometheus/node-exporter:v1.8.2",
		},
		{
			name:        "rejects an empty image",
			component:   "router",
			ref:         v1.ImageRef{Tag: "v1.2.0"},
			expectError: "router image is required",
		},
		{
			name:        "rejects an empty tag",
			component:   "router",
			ref:         v1.ImageRef{Image: "neutree/router"},
			expectError: "router tag is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual, err := BuildProfileImageRef(tt.imagePrefix, tt.component, tt.ref)
			if tt.expectError != "" {
				require.ErrorContains(t, err, tt.expectError)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, actual)
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
		{
			name:        "docker hub leaves image unchanged",
			imagePrefix: "docker.io/neutree",
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

func TestRewriteImageRef(t *testing.T) {
	tests := []struct {
		name        string
		imagePrefix string
		image       string
		expected    string
	}{
		{
			name:        "docker.io image keeps repository path",
			imagePrefix: "registry.example.com/neutree-ai",
			image:       "docker.io/neutree/neutree-node-agent:v1.2.0",
			expected:    "registry.example.com/neutree-ai/neutree/neutree-node-agent:v1.2.0",
		},
		{
			name:        "image without source registry keeps repository path",
			imagePrefix: "registry.example.com/neutree-ai",
			image:       "neutree/neutree-node-agent:v1.2.0",
			expected:    "registry.example.com/neutree-ai/neutree/neutree-node-agent:v1.2.0",
		},
		{
			name:        "already rewritten image is unchanged",
			imagePrefix: "registry.example.com/neutree-ai",
			image:       "registry.example.com/neutree-ai/neutree/neutree-node-agent:v1.2.0",
			expected:    "registry.example.com/neutree-ai/neutree/neutree-node-agent:v1.2.0",
		},
		{
			name:        "empty prefix leaves image unchanged",
			imagePrefix: "",
			image:       "docker.io/neutree/neutree-node-agent:v1.2.0",
			expected:    "docker.io/neutree/neutree-node-agent:v1.2.0",
		},
		{
			name:        "docker hub preserves explicit upstream registry",
			imagePrefix: "docker.io/neutree-ai",
			image:       "registry.k8s.io/kube-state-metrics/kube-state-metrics:v2.15.0",
			expected:    "registry.k8s.io/kube-state-metrics/kube-state-metrics:v2.15.0",
		},
		{
			name:        "docker hub preserves quay image",
			imagePrefix: "docker.io",
			image:       "quay.io/prometheus/node-exporter:v1.8.2",
			expected:    "quay.io/prometheus/node-exporter:v1.8.2",
		},
		{
			name:        "docker hub leaves unqualified image unchanged",
			imagePrefix: "docker.io/neutree-ai",
			image:       "neutree/neutree-node-agent:v1.2.0",
			expected:    "neutree/neutree-node-agent:v1.2.0",
		},
		{
			name:        "empty image stays empty",
			imagePrefix: "registry.example.com/neutree-ai",
			image:       "",
			expected:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RewriteImageRef(tt.imagePrefix, tt.image)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestRelocateImageRef(t *testing.T) {
	tests := []struct {
		name        string
		imagePrefix string
		image       string
		expected    string
	}{
		{
			name:        "source registry is replaced by the target prefix",
			imagePrefix: "registry.example.com/neutree-ai",
			image:       "docker.io/neutree/neutree-node-agent:v1.2.0",
			expected:    "registry.example.com/neutree-ai/neutree/neutree-node-agent:v1.2.0",
		},
		{
			name:        "image without source registry keeps repository path",
			imagePrefix: "registry.example.com/neutree-ai",
			image:       "neutree/neutree-node-agent:v1.2.0",
			expected:    "registry.example.com/neutree-ai/neutree/neutree-node-agent:v1.2.0",
		},
		{
			name:        "already relocated image is unchanged",
			imagePrefix: "registry.example.com/neutree-ai",
			image:       "registry.example.com/neutree-ai/neutree/neutree-node-agent:v1.2.0",
			expected:    "registry.example.com/neutree-ai/neutree/neutree-node-agent:v1.2.0",
		},
		{
			// The pull-side RewriteImageRef leaves these alone; a push must not,
			// or the image lands on the source registry instead of the target.
			name:        "docker hub prefix still relocates an unqualified image",
			imagePrefix: "docker.io/neutree-ai",
			image:       "my-workload:v1",
			expected:    "docker.io/neutree-ai/my-workload:v1",
		},
		{
			name:        "docker hub prefix still replaces a foreign source registry",
			imagePrefix: "docker.io/neutree-ai",
			image:       "ghcr.io/acme/my-workload:v1",
			expected:    "docker.io/neutree-ai/acme/my-workload:v1",
		},
		{
			name:        "empty prefix leaves image unchanged",
			imagePrefix: "",
			image:       "docker.io/neutree/neutree-node-agent:v1.2.0",
			expected:    "docker.io/neutree/neutree-node-agent:v1.2.0",
		},
		{
			name:        "empty image stays empty",
			imagePrefix: "registry.example.com/neutree-ai",
			image:       "",
			expected:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, RelocateImageRef(tt.imagePrefix, tt.image))
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
