package v1

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEngineVersion_GetImageForAccelerator(t *testing.T) {
	tests := []struct {
		name            string
		engineVersion   *EngineVersion
		acceleratorType string
		expected        *EngineImage
	}{
		{
			name: "nvidia-gpu image exists",
			engineVersion: &EngineVersion{
				Version: "v0.5.0",
				Images: map[string]*EngineImage{
					"nvidia-gpu": {
						ImageName: "neutree/vllm-cuda",
						Tag:       "v0.5.0",
					},
				},
			},
			acceleratorType: "nvidia-gpu",
			expected: &EngineImage{
				ImageName: "neutree/vllm-cuda",
				Tag:       "v0.5.0",
			},
		},
		{
			name: "amd-gpu image exists",
			engineVersion: &EngineVersion{
				Version: "v0.5.0",
				Images: map[string]*EngineImage{
					"amd-gpu": {
						ImageName: "neutree/vllm-rocm",
						Tag:       "v0.5.0",
					},
				},
			},
			acceleratorType: "amd-gpu",
			expected: &EngineImage{
				ImageName: "neutree/vllm-rocm",
				Tag:       "v0.5.0",
			},
		},
		{
			name: "accelerator type not found",
			engineVersion: &EngineVersion{
				Version: "v0.5.0",
				Images: map[string]*EngineImage{
					"nvidia-gpu": {
						ImageName: "neutree/vllm-cuda",
						Tag:       "v0.5.0",
					},
				},
			},
			acceleratorType: "intel-gpu",
			expected:        nil,
		},
		{
			name: "images map is nil",
			engineVersion: &EngineVersion{
				Version: "v0.5.0",
			},
			acceleratorType: "nvidia-gpu",
			expected:        nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.engineVersion.GetImageForAccelerator(tt.acceleratorType)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestEngineVersion_SetImage(t *testing.T) {
	ev := &EngineVersion{
		Version: "v0.5.0",
	}

	ev.SetImage("nvidia-gpu", "neutree/vllm-cuda", "v0.5.0")

	assert.NotNil(t, ev.Images)
	assert.NotNil(t, ev.Images["nvidia-gpu"])
	assert.Equal(t, "neutree/vllm-cuda", ev.Images["nvidia-gpu"].ImageName)
	assert.Equal(t, "v0.5.0", ev.Images["nvidia-gpu"].Tag)

	// Add another image
	ev.SetImage("amd-gpu", "neutree/vllm-rocm", "v0.5.0")

	assert.Len(t, ev.Images, 2)
	assert.NotNil(t, ev.Images["amd-gpu"])
	assert.Equal(t, "neutree/vllm-rocm", ev.Images["amd-gpu"].ImageName)
}

func TestEngineVersion_HasImageForAccelerator(t *testing.T) {
	ev := &EngineVersion{
		Version: "v0.5.0",
		Images: map[string]*EngineImage{
			"nvidia-gpu": {
				ImageName: "neutree/vllm-cuda",
				Tag:       "v0.5.0",
			},
		},
	}

	assert.True(t, ev.HasImageForAccelerator("nvidia-gpu"))
	assert.False(t, ev.HasImageForAccelerator("amd-gpu"))
	assert.False(t, ev.HasImageForAccelerator("cpu"))
}

func TestEngineImage_GetFullImagePath(t *testing.T) {
	tests := []struct {
		name              string
		image             *EngineImage
		expectedImageName string
		expectedTag       string
	}{
		{
			name: "valid image",
			image: &EngineImage{
				ImageName: "neutree/vllm-cuda",
				Tag:       "v0.5.0",
			},
			expectedImageName: "neutree/vllm-cuda",
			expectedTag:       "v0.5.0",
		},
		{
			name:              "nil image",
			image:             nil,
			expectedImageName: "",
			expectedTag:       "",
		},
		{
			name: "image without tag",
			image: &EngineImage{
				ImageName: "neutree/vllm-cuda",
			},
			expectedImageName: "neutree/vllm-cuda",
			expectedTag:       "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			imageName, tag := tt.image.GetFullImagePath()
			assert.Equal(t, tt.expectedImageName, imageName)
			assert.Equal(t, tt.expectedTag, tag)
		})
	}
}

func TestEngineVersion_GetSupportedAccelerators(t *testing.T) {
	tests := []struct {
		name          string
		engineVersion *EngineVersion
		expectedCount int
		contains      []string
	}{
		{
			name: "multiple accelerators",
			engineVersion: &EngineVersion{
				Version: "v0.5.0",
				Images: map[string]*EngineImage{
					"nvidia-gpu": {ImageName: "neutree/vllm-cuda", Tag: "v0.5.0"},
					"amd-gpu":    {ImageName: "neutree/vllm-rocm", Tag: "v0.5.0"},
					"cpu":        {ImageName: "neutree/vllm-cpu", Tag: "v0.5.0"},
				},
			},
			expectedCount: 3,
			contains:      []string{"nvidia-gpu", "amd-gpu", "cpu"},
		},
		{
			name: "single accelerator",
			engineVersion: &EngineVersion{
				Version: "v0.5.0",
				Images: map[string]*EngineImage{
					"nvidia-gpu": {ImageName: "neutree/vllm-cuda", Tag: "v0.5.0"},
				},
			},
			expectedCount: 1,
			contains:      []string{"nvidia-gpu"},
		},
		{
			name: "no images",
			engineVersion: &EngineVersion{
				Version: "v0.5.0",
			},
			expectedCount: 0,
			contains:      []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.engineVersion.GetSupportedAccelerators()
			assert.Len(t, result, tt.expectedCount)

			for _, accelerator := range tt.contains {
				assert.Contains(t, result, accelerator)
			}
		})
	}
}

func TestEngineVersion_SupportsAccelerator(t *testing.T) {
	ev := &EngineVersion{
		Version: "v0.5.0",
		Images: map[string]*EngineImage{
			"nvidia-gpu": {ImageName: "neutree/vllm-cuda", Tag: "v0.5.0"},
			"amd-gpu":    {ImageName: "neutree/vllm-rocm", Tag: "v0.5.0"},
		},
	}

	tests := []struct {
		name            string
		acceleratorType string
		expected        bool
	}{
		{"supports nvidia-gpu", "nvidia-gpu", true},
		{"supports amd-gpu", "amd-gpu", true},
		{"does not support cpu", "cpu", false},
		{"does not support intel-gpu", "intel-gpu", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ev.SupportsAccelerator(tt.acceleratorType)
			assert.Equal(t, tt.expected, result)
		})
	}
}
