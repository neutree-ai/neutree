package releaseprofile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestRenderPackageArtifactsUsesExactCatalogMaterial(t *testing.T) {
	builder, err := NewBuilderForCatalog(BuiltinCatalog())
	require.NoError(t, err)

	artifacts, err := RenderPackageArtifacts(builder)
	require.NoError(t, err)
	require.Len(t, artifacts, 12)

	assert.Equal(t, "neutree/neutree-serve:v1.1.1\nneutree/neutree-node-agent:v1.1.0-rc.1\nquay.io/prometheus/node-exporter:v1.8.2\nvictoriametrics/vmagent:v1.115.0\n",
		string(artifacts[filepath.Join("v1.2.0", v1.SSHClusterType, "images.txt")]))
	assert.Equal(t, "neutree/neutree-serve:v1.1.1-rocm\nneutree/neutree-node-agent:v1.1.0-rc.1\nquay.io/prometheus/node-exporter:v1.8.2\nvictoriametrics/vmagent:v1.115.0\n",
		string(artifacts[filepath.Join("v1.2.0", v1.SSHClusterType, "amd_gpu-images.txt")]))
	assert.Equal(t, "neutree/neutree-runtime:v1.1.1\nneutree/router:v1.1.1\nneutree/neutree-node-agent:v1.1.0-rc.1\nquay.io/prometheus/node-exporter:v1.8.2\nvictoriametrics/vmagent:v1.115.0\nregistry.k8s.io/kube-state-metrics/kube-state-metrics:v2.15.0\n",
		string(artifacts[filepath.Join("v1.2.0", v1.KubernetesClusterType, "images.txt")]))

	manifest := string(artifacts[filepath.Join("v1.2.0", "cluster-profile.yaml")])
	assert.Contains(t, manifest, "cluster_profile:")
	assert.Contains(t, manifest, "ssh:")
	assert.Contains(t, manifest, "kubernetes:")
	assert.Contains(t, manifest, "version: v1.2.0")
}

func TestBuilderBuildPackageImagesAppliesCatalogArtifactRule(t *testing.T) {
	builder, err := NewBuilderForCatalog(BuiltinCatalog())
	require.NoError(t, err)

	images, err := builder.BuildPackageImages("v1.2.0", v1.SSHClusterType, "amd_gpu")
	require.NoError(t, err)
	assert.Equal(t, []v1.ImageRef{
		{Image: "neutree/neutree-serve", Tag: "v1.1.1-rocm"},
		{Image: "neutree/neutree-node-agent", Tag: "v1.1.0-rc.1"},
		{Image: "quay.io/prometheus/node-exporter", Tag: "v1.8.2"},
		{Image: "victoriametrics/vmagent", Tag: "v1.115.0"},
	}, images)
	assert.Equal(t, []string{"amd_gpu"}, builder.PackageAccelerators(v1.SSHClusterType))
	assert.Empty(t, builder.PackageAccelerators(v1.KubernetesClusterType))
}

func TestWritePackageArtifactsReplacesPreviousGeneratedTree(t *testing.T) {
	builder, err := NewBuilderForCatalog(BuiltinCatalog())
	require.NoError(t, err)

	output := t.TempDir()
	require.NoError(t, WritePackageArtifacts(output, builder))

	stale := filepath.Join(output, "stale.txt")
	require.NoError(t, os.WriteFile(stale, []byte("stale"), 0o600))

	require.NoError(t, WritePackageArtifacts(output, builder))
	assert.NoFileExists(t, stale)
	assert.FileExists(t, filepath.Join(output, "v1.1.0", "cluster-profile.yaml"))
	assert.FileExists(t, filepath.Join(output, packageArtifactMarkerName))
}

func TestWritePackageArtifactsRejectsUnsafeOutputDirectory(t *testing.T) {
	builder, err := NewBuilderForCatalog(BuiltinCatalog())
	require.NoError(t, err)

	for _, output := range []string{".", "..", string(filepath.Separator)} {
		t.Run(output, func(t *testing.T) {
			err := WritePackageArtifacts(output, builder)
			require.ErrorContains(t, err, "must not be")
		})
	}
}

func TestWritePackageArtifactsRejectsUnownedNonEmptyDirectory(t *testing.T) {
	builder, err := NewBuilderForCatalog(BuiltinCatalog())
	require.NoError(t, err)

	output := filepath.Join(t.TempDir(), "unowned")
	require.NoError(t, os.MkdirAll(output, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(output, "keep.txt"), []byte("keep"), 0o600))

	err = WritePackageArtifacts(output, builder)
	require.ErrorContains(t, err, "must be empty or managed")
	assert.FileExists(t, filepath.Join(output, "keep.txt"))
}
