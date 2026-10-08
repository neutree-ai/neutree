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
				ImageName: "vllm/vllm-cuda",
				Tag:       "v0.5.0",
			},
			expected: "registry.example.com/neutree/vllm/vllm-cuda:v0.5.0",
		},
		{
			name:        "without prefix",
			imagePrefix: "registry.example.com",
			imgSpec: &ImageSpec{
				ImageName: "vllm/vllm-cuda",
				Tag:       "v0.5.0",
			},
			expected: "registry.example.com/vllm/vllm-cuda:v0.5.0",
		},
		{
			name:        "remove existing registry",
			imagePrefix: "new-registry.com/neutree",
			imgSpec: &ImageSpec{
				ImageName: "old-registry.com/vllm/vllm-cuda",
				Tag:       "v0.5.0",
			},
			expected: "new-registry.com/neutree/vllm/vllm-cuda:v0.5.0",
		},
		{
			name:        "remove existing registry with port",
			imagePrefix: "new-registry.com/neutree",
			imgSpec: &ImageSpec{
				ImageName: "old-registry.com:5000/vllm/vllm-cuda",
				Tag:       "v0.5.0",
			},
			expected: "new-registry.com/neutree/vllm/vllm-cuda:v0.5.0",
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
		{
			name:        "push dockerhub official image to custom registry",
			imagePrefix: "registry.example.com",
			imgSpec: &ImageSpec{
				ImageName: "postgres",
				Tag:       "v13.0.0",
			},
			expected: "registry.example.com/library/postgres:v13.0.0",
		},
		{
			name:        "push dockerhub official image to custom registry with namespace",
			imagePrefix: "registry.example.com/neutree",
			imgSpec: &ImageSpec{
				ImageName: "postgres",
				Tag:       "v13.0.0",
			},
			expected: "registry.example.com/neutree/library/postgres:v13.0.0",
		},
		{
			name:        "push explicit dockerhub official image to custom registry",
			imagePrefix: "registry.example.com",
			imgSpec: &ImageSpec{
				ImageName: "docker.io/postgres",
				Tag:       "v13.0.0",
			},
			expected: "registry.example.com/library/postgres:v13.0.0",
		},
		{
			name:        "push index dockerhub official image to custom registry",
			imagePrefix: "registry.example.com",
			imgSpec: &ImageSpec{
				ImageName: "index.docker.io/postgres",
				Tag:       "v13.0.0",
			},
			expected: "registry.example.com/library/postgres:v13.0.0",
		},
		{
			name:        "push registry-1 dockerhub official image to custom registry",
			imagePrefix: "registry.example.com",
			imgSpec: &ImageSpec{
				ImageName: "registry-1.docker.io/postgres",
				Tag:       "v13.0.0",
			},
			expected: "registry.example.com/library/postgres:v13.0.0",
		},
		{
			name:        "push non dockerhub registry image without namespace",
			imagePrefix: "registry.example.com",
			imgSpec: &ImageSpec{
				ImageName: "private.example.com/postgres",
				Tag:       "v13.0.0",
			},
			expected: "registry.example.com/postgres:v13.0.0",
		},
		{
			name:        "push registry port image without namespace",
			imagePrefix: "registry.example.com",
			imgSpec: &ImageSpec{
				ImageName: "private.example.com:5000/postgres",
				Tag:       "v13.0.0",
			},
			expected: "registry.example.com/postgres:v13.0.0",
		},
		{
			name:        "registry with port and nested repository path",
			imagePrefix: "registry.example.com/neutree",
			imgSpec: &ImageSpec{
				ImageName: "harbor.example.cn:5443/team/img",
				Tag:       "v3.0.0",
			},
			expected: "registry.example.com/neutree/team/img:v3.0.0",
		},
		{
			// Packages built before build-package.sh split the reference at the
			// first colon, so the registry port and path landed in the tag.
			// Both spellings must resolve to the same target: a mirror must not
			// end up with the same image under two repositories.
			name:        "legacy manifest storing a registry port in the tag",
			imagePrefix: "registry.example.com/neutree",
			imgSpec: &ImageSpec{
				ImageName: "harbor.example.cn",
				Tag:       "5443/team/img:v3.0.0",
			},
			expected: "registry.example.com/neutree/team/img:v3.0.0",
		},
		{
			// Same legacy spelling for an image that was saved without a tag:
			// the tag field is non-empty, so validation lets it through, but the
			// reference carries no tag of its own. The archive holds it under
			// Docker's implicit latest.
			name:        "legacy manifest for an untagged image from a ported registry",
			imagePrefix: "registry.example.com/neutree",
			imgSpec: &ImageSpec{
				ImageName: "harbor.example.cn",
				Tag:       "5443/team/img",
			},
			expected: "registry.example.com/neutree/team/img:latest",
		},
		{
			name:        "untagged image from a ported registry",
			imagePrefix: "registry.example.com/neutree",
			imgSpec: &ImageSpec{
				ImageName: "harbor.example.cn:5443/team/img",
			},
			expected: "registry.example.com/neutree/team/img:latest",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := pusher.buildTargetImage(tt.imagePrefix, tt.imgSpec)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestImagePusherBuildImageRef(t *testing.T) {
	tests := []struct {
		name     string
		imgSpec  *ImageSpec
		expected string
	}{
		{
			name:     "name and tag",
			imgSpec:  &ImageSpec{ImageName: "neutree/router", Tag: "v1.2.1-alpha.1"},
			expected: "neutree/router:v1.2.1-alpha.1",
		},
		{
			name:     "registry with port and nested repository path",
			imgSpec:  &ImageSpec{ImageName: "harbor.example.cn:5443/team/img", Tag: "v3.0.0"},
			expected: "harbor.example.cn:5443/team/img:v3.0.0",
		},
		{
			// The archive holds the image under this reference, so the tag
			// command has to name it exactly as the legacy manifest spelled it.
			name:     "legacy manifest storing a registry port in the tag",
			imgSpec:  &ImageSpec{ImageName: "harbor.example.cn", Tag: "5443/team/img:v3.0.0"},
			expected: "harbor.example.cn:5443/team/img:v3.0.0",
		},
		{
			name:     "no tag",
			imgSpec:  &ImageSpec{ImageName: "neutree/router"},
			expected: "neutree/router",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, buildImageRef(tt.imgSpec))
		})
	}
}

func TestSplitImageTag(t *testing.T) {
	tests := []struct {
		name         string
		ref          string
		expectedName string
		expectedTag  string
	}{
		{
			name:         "name and tag",
			ref:          "neutree/router:v1.2.1-alpha.1",
			expectedName: "neutree/router",
			expectedTag:  "v1.2.1-alpha.1",
		},
		{
			name:         "registry with port",
			ref:          "harbor.example.cn:5443/team/img:v3.0.0",
			expectedName: "harbor.example.cn:5443/team/img",
			expectedTag:  "v3.0.0",
		},
		{
			name:         "registry with port and no tag",
			ref:          "harbor.example.cn:5443/team/img",
			expectedName: "harbor.example.cn:5443/team/img",
			expectedTag:  "",
		},
		{
			name:         "no tag",
			ref:          "postgres",
			expectedName: "postgres",
			expectedTag:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, tag := splitImageTag(tt.ref)
			assert.Equal(t, tt.expectedName, name)
			assert.Equal(t, tt.expectedTag, tag)
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
