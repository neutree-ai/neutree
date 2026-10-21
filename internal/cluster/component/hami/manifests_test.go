package hami

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	kubeversion "k8s.io/apimachinery/pkg/version"

	v1 "github.com/neutree-ai/neutree/api/v1"
	clusterversion "github.com/neutree-ai/neutree/internal/cluster/version"
	componentversion "github.com/neutree-ai/neutree/internal/component"
)

func TestHAMiKubernetesVersionDiscoveryCompatibility(test *testing.T) {
	cases := []struct {
		name                   string
		server                 *kubeversion.Info
		discoveryError         error
		serverVersion          string
		minor                  string
		schedulerVersion       string
		legacySchedulerVersion string
		chartVersion           string
	}{
		{
			name: "GKE prerelease suffix", server: &kubeversion.Info{GitVersion: "v1.33.4-gke.123"},
			serverVersion: "v1.33.4", minor: "1.33", schedulerVersion: "v1.33.13", chartVersion: "v1.33.4",
			legacySchedulerVersion: "v1.33.4",
		},
		{
			name: "K3s build metadata", server: &kubeversion.Info{GitVersion: "v1.34.2+k3s1"},
			serverVersion: "v1.34.2", minor: "1.34", schedulerVersion: "v1.34.12", chartVersion: "v1.34.2",
			legacySchedulerVersion: "v1.34.2",
		},
		{
			name: "numeric minor with plus", server: &kubeversion.Info{Major: "1", Minor: "35+"},
			serverVersion: "v1.35.0", minor: "1.35", schedulerVersion: "v1.35.9", chartVersion: "v1.35.0",
			legacySchedulerVersion: "v1.35.0",
		},
		{
			name: "mapped minor preserves chart patch", server: &kubeversion.Info{GitVersion: "v1.35.4-vendor.1"},
			serverVersion: "v1.35.4", minor: "1.35", schedulerVersion: "v1.35.9", chartVersion: "v1.35.4",
			legacySchedulerVersion: "v1.35.4",
		},
		{
			name: "EKS fallback fields", server: &kubeversion.Info{GitVersion: "invalid", Major: "v1", Minor: "36-eks-123"},
			serverVersion: "v1.36.0", minor: "1.36", schedulerVersion: "v1.36.0", chartVersion: "v1.36.0",
		},
		{
			name: "unmapped active minor preserves server patch", server: &kubeversion.Info{GitVersion: "v1.37.0-vendor.1"},
			serverVersion: "v1.37.0", minor: "1.37", schedulerVersion: "v1.37.0", chartVersion: "v1.37.0",
		},
		{
			name: "older EOL minor remains unmapped", server: &kubeversion.Info{GitVersion: "v1.25.10+vendor.1"},
			serverVersion: "v1.25.10", minor: "1.25", schedulerVersion: "v1.25.10", chartVersion: "v1.25.10",
		},
		{
			name: "unmapped minor uses detected version", server: &kubeversion.Info{GitVersion: "v1.40.2-vendor.1"},
			serverVersion: "v1.40.2", minor: "1.40", schedulerVersion: "v1.40.2", chartVersion: "v1.40.2",
		},
		{
			name: "invalid major", server: &kubeversion.Info{Major: "invalid", Minor: "37+"},
			schedulerVersion: clusterversion.DefaultKubeSchedulerVersion, chartVersion: clusterversion.DefaultKubeSchedulerVersion,
		},
		{
			name: "invalid minor", server: &kubeversion.Info{Major: "1", Minor: "invalid"},
			schedulerVersion: clusterversion.DefaultKubeSchedulerVersion, chartVersion: clusterversion.DefaultKubeSchedulerVersion,
		},
		{
			name: "discovery unavailable", discoveryError: errors.New("discovery unavailable"),
			schedulerVersion: clusterversion.DefaultKubeSchedulerVersion, chartVersion: clusterversion.DefaultKubeSchedulerVersion,
		},
	}

	originalGetKubernetesServerVersion := getKubernetesServerVersion
	test.Cleanup(func() {
		getKubernetesServerVersion = originalGetKubernetesServerVersion
	})

	for _, testCase := range cases {
		test.Run(testCase.name, func(test *testing.T) {
			getKubernetesServerVersion = func(*v1.Cluster) (*kubeversion.Info, error) {
				return testCase.server, testCase.discoveryError
			}
			component := NewHAMiComponent(newTestCluster(), "neutree-system", "docker.io",
				"", v1.KubernetesClusterConfig{}, newHAMiFakeClient(test))

			assert.Equal(test, testCase.serverVersion, clusterversion.KubernetesServerVersion(testCase.server))
			assert.Equal(test, testCase.minor, clusterversion.KubernetesServerMajorMinor(testCase.server))
			assert.Equal(test, testCase.schedulerVersion, component.resolveKubeSchedulerVersion(componentversion.HAMi))
			legacySchedulerVersion := testCase.legacySchedulerVersion
			if legacySchedulerVersion == "" {
				legacySchedulerVersion = testCase.schedulerVersion
			}
			assert.Equal(test, legacySchedulerVersion, component.resolveKubeSchedulerVersion(componentversion.LegacyHAMi))
			assert.Equal(test, testCase.chartVersion, component.resolveChartKubeVersion().Version)
		})
	}
}
