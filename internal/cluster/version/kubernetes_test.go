package version

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"helm.sh/helm/v3/pkg/chartutil"
	kubeversion "k8s.io/apimachinery/pkg/version"

	"github.com/neutree-ai/neutree/internal/component"
)

func TestDefaultKubeSchedulerVersion(test *testing.T) {
	assert.Equal(test, "v1.32.13", DefaultKubeSchedulerVersion)
}

func TestKubeSchedulerVersionsByMinor(test *testing.T) {
	cases := []struct {
		minor                  string
		serverVersion          string
		schedulerVersion       string
		legacySchedulerVersion string
	}{
		{minor: "1.26", serverVersion: "v1.26.1", schedulerVersion: "v1.26.15", legacySchedulerVersion: "v1.26.15"},
		{minor: "1.27", serverVersion: "v1.27.1", schedulerVersion: "v1.27.16", legacySchedulerVersion: "v1.27.16"},
		{minor: "1.28", serverVersion: "v1.28.1", schedulerVersion: "v1.28.15", legacySchedulerVersion: "v1.28.15"},
		{minor: "1.29", serverVersion: "v1.29.1", schedulerVersion: "v1.29.14", legacySchedulerVersion: "v1.29.14"},
		{minor: "1.30", serverVersion: "v1.30.1", schedulerVersion: "v1.30.14", legacySchedulerVersion: "v1.30.14"},
		{minor: "1.31", serverVersion: "v1.31.1", schedulerVersion: "v1.31.14", legacySchedulerVersion: "v1.31.14"},
		{minor: "1.32", serverVersion: "v1.32.1", schedulerVersion: "v1.32.13", legacySchedulerVersion: "v1.32.13"},
		{minor: "1.33", serverVersion: "v1.33.4", schedulerVersion: "v1.33.13", legacySchedulerVersion: "v1.33.4"},
		{minor: "1.34", serverVersion: "v1.34.2", schedulerVersion: "v1.34.12", legacySchedulerVersion: "v1.34.2"},
		{minor: "1.35", serverVersion: "v1.35.4", schedulerVersion: "v1.35.9", legacySchedulerVersion: "v1.35.4"},
	}

	assert.Len(test, KubeSchedulerVersionsByMinor, len(cases))
	for _, testCase := range cases {
		test.Run(testCase.minor, func(test *testing.T) {
			require.Contains(test, KubeSchedulerVersionsByMinor, testCase.minor)
			assert.Equal(test, testCase.schedulerVersion, KubeSchedulerVersionsByMinor[testCase.minor])
			serverVersion := &kubeversion.Info{GitVersion: testCase.serverVersion}
			assert.Equal(test, testCase.schedulerVersion, KubeSchedulerVersionForHAMi(component.HAMi, serverVersion))
			assert.Equal(test, testCase.legacySchedulerVersion, KubeSchedulerVersionForHAMi(component.LegacyHAMi, serverVersion))
		})
	}
}

func TestKubernetesVersionDiscoveryCompatibility(test *testing.T) {
	defaultChartVersion := chartutil.KubeVersion{Version: "v1.32.13", Major: "1", Minor: "32"}
	cases := []struct {
		name                   string
		server                 *kubeversion.Info
		serverVersion          string
		majorMinor             string
		schedulerVersion       string
		legacySchedulerVersion string
		chartVersion           chartutil.KubeVersion
	}{
		{
			name: "GKE prerelease suffix", server: &kubeversion.Info{GitVersion: "v1.33.4-gke.123"},
			serverVersion: "v1.33.4", majorMinor: "1.33", schedulerVersion: "v1.33.13", legacySchedulerVersion: "v1.33.4",
			chartVersion: chartutil.KubeVersion{Version: "v1.33.4", Major: "1", Minor: "33"},
		},
		{
			name: "K3s build metadata", server: &kubeversion.Info{GitVersion: "v1.34.2+k3s1"},
			serverVersion: "v1.34.2", majorMinor: "1.34", schedulerVersion: "v1.34.12", legacySchedulerVersion: "v1.34.2",
			chartVersion: chartutil.KubeVersion{Version: "v1.34.2", Major: "1", Minor: "34"},
		},
		{
			name: "numeric minor with plus", server: &kubeversion.Info{Major: "1", Minor: "35+"},
			serverVersion: "v1.35.0", majorMinor: "1.35", schedulerVersion: "v1.35.9", legacySchedulerVersion: "v1.35.0",
			chartVersion: chartutil.KubeVersion{Version: "v1.35.0", Major: "1", Minor: "35"},
		},
		{
			name: "mapped minor preserves chart patch", server: &kubeversion.Info{GitVersion: "v1.35.4-vendor.1"},
			serverVersion: "v1.35.4", majorMinor: "1.35", schedulerVersion: "v1.35.9", legacySchedulerVersion: "v1.35.4",
			chartVersion: chartutil.KubeVersion{Version: "v1.35.4", Major: "1", Minor: "35"},
		},
		{
			name: "EKS fallback fields", server: &kubeversion.Info{GitVersion: "invalid", Major: "v1", Minor: "36-eks-123"},
			serverVersion: "v1.36.0", majorMinor: "1.36", schedulerVersion: "v1.36.0", legacySchedulerVersion: "v1.36.0",
			chartVersion: chartutil.KubeVersion{Version: "v1.36.0", Major: "1", Minor: "36"},
		},
		{
			name: "unmapped active minor", server: &kubeversion.Info{GitVersion: "v1.37.0-vendor.1"},
			serverVersion: "v1.37.0", majorMinor: "1.37", schedulerVersion: "v1.37.0", legacySchedulerVersion: "v1.37.0",
			chartVersion: chartutil.KubeVersion{Version: "v1.37.0", Major: "1", Minor: "37"},
		},
		{
			name: "older EOL minor remains unmapped", server: &kubeversion.Info{GitVersion: "v1.25.10+vendor.1"},
			serverVersion: "v1.25.10", majorMinor: "1.25", schedulerVersion: "v1.25.10", legacySchedulerVersion: "v1.25.10",
			chartVersion: chartutil.KubeVersion{Version: "v1.25.10", Major: "1", Minor: "25"},
		},
		{
			name: "future unmapped minor preserves patch", server: &kubeversion.Info{GitVersion: "v1.40.2-vendor.1+build.1"},
			serverVersion: "v1.40.2", majorMinor: "1.40", schedulerVersion: "v1.40.2", legacySchedulerVersion: "v1.40.2",
			chartVersion: chartutil.KubeVersion{Version: "v1.40.2", Major: "1", Minor: "40"},
		},
		{
			name: "GitVersion without prefix", server: &kubeversion.Info{GitVersion: "1.31.8+vendor.1"},
			serverVersion: "1.31.8", majorMinor: "1.31", schedulerVersion: "v1.31.14", legacySchedulerVersion: "v1.31.14",
			chartVersion: chartutil.KubeVersion{Version: "1.31.8", Major: "1", Minor: "31"},
		},
		{
			name: "valid GitVersion wins over invalid fields", server: &kubeversion.Info{GitVersion: "v1.30.7", Major: "invalid", Minor: "invalid"},
			serverVersion: "v1.30.7", majorMinor: "1.30", schedulerVersion: "v1.30.14", legacySchedulerVersion: "v1.30.14",
			chartVersion: chartutil.KubeVersion{Version: "v1.30.7", Major: "1", Minor: "30"},
		},
		{
			name: "invalid GitVersion uses mapped fallback fields", server: &kubeversion.Info{GitVersion: "invalid", Major: "v1", Minor: "32+"},
			serverVersion: "v1.32.0", majorMinor: "1.32", schedulerVersion: "v1.32.13", legacySchedulerVersion: "v1.32.13",
			chartVersion: chartutil.KubeVersion{Version: "v1.32.0", Major: "1", Minor: "32"},
		},
		{
			name: "invalid major", server: &kubeversion.Info{Major: "invalid", Minor: "37+"},
			schedulerVersion: "v1.32.13", legacySchedulerVersion: "v1.32.13", chartVersion: defaultChartVersion,
		},
		{
			name: "invalid minor", server: &kubeversion.Info{Major: "1", Minor: "invalid"},
			schedulerVersion: "v1.32.13", legacySchedulerVersion: "v1.32.13", chartVersion: defaultChartVersion,
		},
		{
			name: "invalid GitVersion without fallback fields", server: &kubeversion.Info{GitVersion: "invalid"},
			schedulerVersion: "v1.32.13", legacySchedulerVersion: "v1.32.13", chartVersion: defaultChartVersion,
		},
		{
			name: "empty server info", server: &kubeversion.Info{},
			schedulerVersion: "v1.32.13", legacySchedulerVersion: "v1.32.13", chartVersion: defaultChartVersion,
		},
		{
			name:             "nil server info",
			schedulerVersion: "v1.32.13", legacySchedulerVersion: "v1.32.13", chartVersion: defaultChartVersion,
		},
	}

	for _, testCase := range cases {
		test.Run(testCase.name, func(test *testing.T) {
			assert.Equal(test, testCase.serverVersion, KubernetesServerVersion(testCase.server))
			assert.Equal(test, testCase.majorMinor, KubernetesServerMajorMinor(testCase.server))
			assert.Equal(test, testCase.schedulerVersion, KubeSchedulerVersionForHAMi(component.HAMi, testCase.server))
			assert.Equal(test, testCase.legacySchedulerVersion, KubeSchedulerVersionForHAMi(component.LegacyHAMi, testCase.server))
			assert.Equal(test, testCase.chartVersion, KubeVersionFromServerVersion(testCase.server))
		})
	}
}

func TestKubeVersionFromBaseVersion(test *testing.T) {
	cases := []struct {
		name         string
		baseVersion  string
		chartVersion chartutil.KubeVersion
	}{
		{
			name:         "empty version preserves input",
			chartVersion: chartutil.KubeVersion{Version: "", Major: "1", Minor: "32"},
		},
		{
			name: "prefixed version", baseVersion: "v1.26.9",
			chartVersion: chartutil.KubeVersion{Version: "v1.26.9", Major: "1", Minor: "26"},
		},
		{
			name: "version without prefix", baseVersion: "1.33.3",
			chartVersion: chartutil.KubeVersion{Version: "1.33.3", Major: "1", Minor: "33"},
		},
		{
			name: "prerelease and metadata remain unchanged", baseVersion: "v1.34.1-rc.1+build.2",
			chartVersion: chartutil.KubeVersion{Version: "v1.34.1-rc.1+build.2", Major: "1", Minor: "34"},
		},
		{
			name: "minor shorthand remains unchanged", baseVersion: "v1.34",
			chartVersion: chartutil.KubeVersion{Version: "v1.34", Major: "1", Minor: "34"},
		},
		{
			name: "future major", baseVersion: "v2.4.6",
			chartVersion: chartutil.KubeVersion{Version: "v2.4.6", Major: "2", Minor: "4"},
		},
		{
			name: "invalid version preserves input", baseVersion: "invalid",
			chartVersion: chartutil.KubeVersion{Version: "invalid", Major: "1", Minor: "32"},
		},
		{
			name: "whitespace is not trimmed", baseVersion: " v1.35.4 ",
			chartVersion: chartutil.KubeVersion{Version: " v1.35.4 ", Major: "1", Minor: "32"},
		},
	}

	for _, testCase := range cases {
		test.Run(testCase.name, func(test *testing.T) {
			assert.Equal(test, testCase.chartVersion, KubeVersionFromBaseVersion(testCase.baseVersion))
		})
	}
}
