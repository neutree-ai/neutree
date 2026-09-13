package packageimport

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMultiArchPlatform(t *testing.T) {
	tests := []struct {
		platform string
		wantArch string
		wantErr  string
	}{
		{platform: "linux/amd64", wantArch: "amd64"},
		{platform: "linux/arm64", wantArch: "arm64"},
		{platform: "", wantErr: "platform is required"},
		{platform: "linux/arm/v7", wantErr: "unsupported platform"},
		{platform: "windows/amd64", wantErr: "unsupported platform"},
	}

	for _, tt := range tests {
		t.Run(tt.platform, func(t *testing.T) {
			platform, err := multiArchPlatform(tt.platform)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, "linux", platform.OS)
			assert.Equal(t, tt.wantArch, platform.Architecture)
		})
	}
}

func TestImageUsesMultiArchPush(t *testing.T) {
	assert.False(t, imageUsesMultiArchPush(false, "linux/amd64"))
	assert.False(t, imageUsesMultiArchPush(true, ""))
	assert.True(t, imageUsesMultiArchPush(true, "linux/amd64"))
}

func TestMultiArchChildTarget(t *testing.T) {
	tests := []struct {
		name     string
		target   string
		platform string
		want     string
	}{
		{
			name:     "nested repository appends architecture to final component",
			target:   "registry.example.com/neutree/neutree-api:v1.2.0",
			platform: "linux/amd64",
			want:     "registry.example.com/neutree/neutree-api-amd64:v1.2.0",
		},
		{
			name:     "repository tag is preserved for arm64",
			target:   "registry.example.com/project/team/component:release-7",
			platform: "linux/arm64",
			want:     "registry.example.com/project/team/component-arm64:release-7",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			platform, err := multiArchPlatform(tt.platform)
			require.NoError(t, err)

			got, err := multiArchChildTarget(tt.target, platform)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestPublishMultiArchIndexMergesAndReplacesPlatform(t *testing.T) {
	server := httptest.NewServer(registry.New())
	defer server.Close()

	host := strings.TrimPrefix(server.URL, "http://")
	logicalTarget := host + "/project/component:v1"
	amd64Target := host + "/project/component-amd64:v1"
	arm64Target := host + "/project/component-arm64:v1"
	amd64 := v1.Platform{OS: "linux", Architecture: "amd64"}
	arm64 := v1.Platform{OS: "linux", Architecture: "arm64"}

	firstAMD64 := platformImage(t, amd64)
	writeImage(t, amd64Target, firstAMD64)
	require.NoError(t, publishMultiArchIndex(context.Background(), insecureTag(t, logicalTarget), insecureTag(t, amd64Target), amd64, "", ""))

	arm64Image := platformImage(t, arm64)
	writeImage(t, arm64Target, arm64Image)
	require.NoError(t, publishMultiArchIndex(context.Background(), insecureTag(t, logicalTarget), insecureTag(t, arm64Target), arm64, "", ""))

	replacementAMD64 := platformImage(t, amd64)
	writeImage(t, amd64Target, replacementAMD64)
	require.NoError(t, publishMultiArchIndex(context.Background(), insecureTag(t, logicalTarget), insecureTag(t, amd64Target), amd64, "", ""))

	logicalRef, err := name.NewTag(logicalTarget, name.Insecure)
	require.NoError(t, err)
	index, err := remote.Index(logicalRef)
	require.NoError(t, err)
	manifest, err := index.IndexManifest()
	require.NoError(t, err)
	require.Len(t, manifest.Manifests, 2)

	assertDescriptorDigest(t, manifest.Manifests, amd64, replacementAMD64)
	assertDescriptorDigest(t, manifest.Manifests, arm64, arm64Image)
}

func TestPublishMultiArchIndexMigratesLegacyManifest(t *testing.T) {
	server := httptest.NewServer(registry.New())
	defer server.Close()

	host := strings.TrimPrefix(server.URL, "http://")
	logicalTarget := host + "/project/component:v1"
	amd64Target := host + "/project/component-amd64:v1"
	amd64 := v1.Platform{OS: "linux", Architecture: "amd64"}
	arm64 := v1.Platform{OS: "linux", Architecture: "arm64"}

	legacyARM64 := platformImage(t, arm64)
	writeImage(t, logicalTarget, legacyARM64)
	amd64Image := platformImage(t, amd64)
	writeImage(t, amd64Target, amd64Image)

	require.NoError(t, publishMultiArchIndex(context.Background(), insecureTag(t, logicalTarget), insecureTag(t, amd64Target), amd64, "", ""))

	logicalRef, err := name.NewTag(logicalTarget, name.Insecure)
	require.NoError(t, err)
	index, err := remote.Index(logicalRef)
	require.NoError(t, err)
	manifest, err := index.IndexManifest()
	require.NoError(t, err)
	require.Len(t, manifest.Manifests, 2)

	assertDescriptorDigest(t, manifest.Manifests, amd64, amd64Image)
	assertDescriptorDigest(t, manifest.Manifests, arm64, legacyARM64)
}

func TestPublishMultiArchIndexRejectsPlatformMismatch(t *testing.T) {
	server := httptest.NewServer(registry.New())
	defer server.Close()

	host := strings.TrimPrefix(server.URL, "http://")
	logicalTarget := host + "/project/component:v1"
	amd64Target := host + "/project/component-amd64:v1"
	amd64 := v1.Platform{OS: "linux", Architecture: "amd64"}
	arm64 := v1.Platform{OS: "linux", Architecture: "arm64"}

	writeImage(t, amd64Target, platformImage(t, arm64))
	err := publishMultiArchIndex(context.Background(), insecureTag(t, logicalTarget), insecureTag(t, amd64Target), amd64, "", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has platform linux/arm64, expected linux/amd64")

	logicalRef, err := name.NewTag(logicalTarget, name.Insecure)
	require.NoError(t, err)
	_, err = remote.Get(logicalRef)
	assert.True(t, isRegistryNotFound(err))
}

func TestPublishMultiArchIndexAcceptsArm64Variant(t *testing.T) {
	server := httptest.NewServer(registry.New())
	defer server.Close()

	host := strings.TrimPrefix(server.URL, "http://")
	logicalTarget := host + "/project/component:v1"
	arm64Target := host + "/project/component-arm64:v1"
	requested := v1.Platform{OS: "linux", Architecture: "arm64"}
	actual := v1.Platform{OS: "linux", Architecture: "arm64", Variant: "v8"}
	image := platformImage(t, actual)
	writeImage(t, arm64Target, image)

	require.NoError(t, publishMultiArchIndex(context.Background(), insecureTag(t, logicalTarget), insecureTag(t, arm64Target), requested, "", ""))

	logicalRef, err := name.NewTag(logicalTarget, name.Insecure)
	require.NoError(t, err)
	index, err := remote.Index(logicalRef)
	require.NoError(t, err)
	manifest, err := index.IndexManifest()
	require.NoError(t, err)
	require.Len(t, manifest.Manifests, 1)
	assertDescriptorDigest(t, manifest.Manifests, actual, image)
}

func platformImage(t *testing.T, platform v1.Platform) v1.Image {
	t.Helper()
	image, err := random.Image(1024, 1)
	require.NoError(t, err)
	config, err := image.ConfigFile()
	require.NoError(t, err)
	config.OS = platform.OS
	config.Architecture = platform.Architecture
	config.Variant = platform.Variant
	image, err = mutate.ConfigFile(image, config)
	require.NoError(t, err)
	return image
}

func writeImage(t *testing.T, target string, image v1.Image) {
	t.Helper()
	require.NoError(t, remote.Write(insecureTag(t, target), image))
}

func insecureTag(t *testing.T, target string) name.Tag {
	t.Helper()
	ref, err := name.NewTag(target, name.Insecure)
	require.NoError(t, err)
	return ref
}

func assertDescriptorDigest(t *testing.T, descriptors []v1.Descriptor, platform v1.Platform, image v1.Image) {
	t.Helper()
	want, err := image.Digest()
	require.NoError(t, err)
	for _, descriptor := range descriptors {
		if descriptor.Platform != nil && descriptor.Platform.Equals(platform) {
			assert.Equal(t, want, descriptor.Digest)
			return
		}
	}
	t.Fatalf("platform %s/%s descriptor not found", platform.OS, platform.Architecture)
}
