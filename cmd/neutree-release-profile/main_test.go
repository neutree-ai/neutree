package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/cli/packageimport"
	"github.com/neutree-ai/neutree/pkg/releaseprofile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeClusterType(t *testing.T) {
	clusterType, err := normalizeClusterType("k8s")
	require.NoError(t, err)
	assert.Equal(t, v1.KubernetesClusterType, clusterType)
}

func TestClusterProfileImagesAddsROCMSuffixForAMDSSHPackage(t *testing.T) {
	profile, err := releaseprofile.CommunityClusterProfile("v1.2.0", v1.SSHClusterType)
	require.NoError(t, err)

	images, err := clusterProfileImages(profile, "amd_gpu")
	require.NoError(t, err)
	assert.Contains(t, images, "neutree/neutree-serve:v1.1.1-rocm")
	assert.NotContains(t, images, "neutree/neutree-serve:v1.1.1")
}

func TestManifestProfileOmitsEmptyComponents(t *testing.T) {
	profile, err := releaseprofile.CommunityClusterProfile("v1.2.0", v1.KubernetesClusterType)
	require.NoError(t, err)

	rendered := manifestProfile(profile)
	require.NotNil(t, rendered.Components.KubernetesRuntime)
	assert.Nil(t, rendered.Components.RayRuntime)
}

func TestRunYAMLUsesCanonicalClusterType(t *testing.T) {
	var output bytes.Buffer
	err := run("v1.2.0", "k8s", "", "yaml", &output)
	require.NoError(t, err)
	assert.Contains(t, output.String(), "cluster_type: kubernetes")
	assert.Contains(t, output.String(), "cluster_profile:")
}

func TestRunYAMLOutputPassesPackageProfileValidation(t *testing.T) {
	var output bytes.Buffer
	require.NoError(t, run("v1.2.0", "k8s", "", "yaml", &output))

	manifestPath := filepath.Join(t.TempDir(), packageimport.ManifestFileName)
	require.NoError(t, os.WriteFile(
		manifestPath,
		[]byte("manifest_version: \"1.0\"\n"+output.String()),
		0o600,
	))

	manifest, err := packageimport.NewParser().ParseManifestFile(manifestPath)
	require.NoError(t, err)
	require.NotNil(t, manifest.ClusterProfile)
	assert.Equal(t, "v1.2.0", manifest.ClusterProfile.Version)
	assert.Equal(t, v1.KubernetesClusterType, manifest.ClusterProfile.ClusterType)
	assert.Equal(t, "v1.1.1", manifest.ClusterProfile.Components.KubernetesRuntime.Tag)
}

func TestRunImagesOutputsOneImagePerLine(t *testing.T) {
	var output bytes.Buffer
	err := run("v1.2.0", "ssh", "", "images", &output)
	require.NoError(t, err)

	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	assert.Contains(t, lines, "neutree/neutree-serve:v1.1.1")
	assert.Contains(t, lines, "neutree/neutree-node-agent:v1.1.0-rc.1")
}
