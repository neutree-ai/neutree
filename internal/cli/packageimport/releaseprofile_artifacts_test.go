package packageimport

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/neutree-ai/neutree/pkg/releaseprofile"
)

func TestGeneratedClusterProfilePassesPackageManifestValidation(t *testing.T) {
	builder, err := releaseprofile.NewBuilderForCatalog(releaseprofile.BuiltinCatalog())
	require.NoError(t, err)

	artifacts, err := releaseprofile.RenderPackageArtifacts(builder)
	require.NoError(t, err)

	manifestPath := filepath.Join(t.TempDir(), ManifestFileName)
	manifest := append([]byte(`manifest_version: "1.0"
images:
  - image_name: "neutree/neutree-serve"
    tag: "v1.1.1"
    image_file: "images/all-images.tar"
`), artifacts[filepath.Join("v1.2.0", "cluster-profile.yaml")]...)
	require.NoError(t, os.WriteFile(manifestPath, manifest, 0o600))

	parsed, err := NewParser().ParseManifestFile(manifestPath)
	require.NoError(t, err)
	require.Len(t, parsed.Images, 1)
	require.NotNil(t, parsed.ClusterProfile)
	assert.Equal(t, "v1.2.0", parsed.ClusterProfile.Version)
}
