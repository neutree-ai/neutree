package releaseprofile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderPackageArtifactsUsesExactCatalogMaterial(t *testing.T) {
	builder, err := NewBuilderForCatalog(BuiltinCatalog())
	require.NoError(t, err)

	artifacts, err := RenderPackageArtifacts(builder)
	require.NoError(t, err)
	require.Len(t, artifacts, 12)

	assert.Equal(t, "neutree/neutree-serve:v1.1.1\nneutree/neutree-node-agent:v1.1.0-rc.1\nquay.io/prometheus/node-exporter:v1.8.2\nvictoriametrics/vmagent:v1.115.0\n",
		string(artifacts[filepath.Join("v1.2.0", "ssh", "images.txt")]))
	assert.Equal(t, "neutree/neutree-serve:v1.1.1-rocm\nneutree/neutree-node-agent:v1.1.0-rc.1\nquay.io/prometheus/node-exporter:v1.8.2\nvictoriametrics/vmagent:v1.115.0\n",
		string(artifacts[filepath.Join("v1.2.0", "ssh", "amd_gpu-images.txt")]))
	assert.Equal(t, "neutree/neutree-runtime:v1.1.1\nneutree/router:v1.1.1\nneutree/neutree-node-agent:v1.1.0-rc.1\nquay.io/prometheus/node-exporter:v1.8.2\nvictoriametrics/vmagent:v1.115.0\nregistry.k8s.io/kube-state-metrics/kube-state-metrics:v2.15.0\n",
		string(artifacts[filepath.Join("v1.2.0", "kubernetes", "images.txt")]))

	manifest := string(artifacts[filepath.Join("v1.2.0", "cluster-profile.yaml")])
	assert.Contains(t, manifest, "cluster_profile:")
	assert.Contains(t, manifest, "ssh:")
	assert.Contains(t, manifest, "kubernetes:")
	assert.Contains(t, manifest, "version: v1.2.0")
}

func TestWritePackageArtifactsReplacesPreviousGeneratedTree(t *testing.T) {
	builder, err := NewBuilderForCatalog(BuiltinCatalog())
	require.NoError(t, err)
	output := t.TempDir()
	stale := filepath.Join(output, "stale.txt")
	require.NoError(t, os.WriteFile(stale, []byte("stale"), 0o600))

	require.NoError(t, WritePackageArtifacts(output, builder))
	assert.NoFileExists(t, stale)
	assert.FileExists(t, filepath.Join(output, "v1.1.0", "cluster-profile.yaml"))
}
