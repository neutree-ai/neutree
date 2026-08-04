package releaseinfo

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestNewSeed(t *testing.T) {
	testCases := []struct {
		name             string
		buildIdentity    string
		wantChannel      v1.ReleaseInfoChannel
		wantBaseline     string
		wantRevisionSame bool
	}{
		{
			name:          "stable",
			buildIdentity: "v1.2.0",
			wantChannel:   v1.ReleaseInfoChannelStable,
			wantBaseline:  "v1.2.0",
		},
		{
			name:          "nightly",
			buildIdentity: "v1.2.0-nightly.20260804",
			wantChannel:   v1.ReleaseInfoChannelNightly,
			wantBaseline:  "v1.2.0",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			seed, err := NewSeed(testCase.buildIdentity)
			require.NoError(t, err)
			require.NotNil(t, seed.Metadata)
			require.NotNil(t, seed.Spec)
			require.NotNil(t, seed.Status)

			assert.Equal(t, testCase.wantBaseline, seed.Metadata.Name)
			assert.Equal(t, testCase.wantChannel, seed.Spec.Channel)
			assert.Equal(t, testCase.buildIdentity, seed.Spec.BuildIdentity)
			assert.NotEmpty(t, seed.Status.Revision)
			assertReleaseMatrix(t, seed)
		})
	}
}

func TestNewSeedRevisionIsDeterministic(t *testing.T) {
	first, err := NewSeed("v1.2.0-nightly.20260804")
	require.NoError(t, err)
	second, err := NewSeed("v1.2.0-nightly.20260804")
	require.NoError(t, err)

	assert.Equal(t, first.Status.Revision, second.Status.Revision)
}

func assertReleaseMatrix(t *testing.T, seed *v1.ReleaseInfo) {
	t.Helper()

	versions := make(map[string]v1.ReleaseInfoClusterVersion, len(seed.Spec.ClusterVersions))
	for _, version := range seed.Spec.ClusterVersions {
		versions[version.Version] = version
	}

	require.Len(t, versions, 3)
	assert.Equal(t, []string{"v1.1.1", "v1.2.0"}, versions["v1.1.0"].UpgradeTo)
	assert.Equal(t, []string{"v1.2.0"}, versions["v1.1.1"].UpgradeTo)
	assert.Empty(t, versions["v1.2.0"].UpgradeTo)

	assert.Equal(t, "neutree/neutree-serve:v1.1.0", versions["v1.1.0"].Components["ray_runtime"])
	assert.Equal(t, "neutree/router:v1.1.0", versions["v1.1.0"].Components["router"])
	assert.Equal(t, "neutree/neutree-node-agent:v1.1.0-alpha.8", versions["v1.1.0"].Components["node_agent"])

	assert.Equal(t, "neutree/neutree-serve:v1.1.1", versions["v1.1.1"].Components["ray_runtime"])
	assert.Equal(t, "neutree/router:v1.1.1", versions["v1.1.1"].Components["router"])
	assert.Equal(t, "neutree/neutree-node-agent:v1.1.0-rc.1", versions["v1.1.1"].Components["node_agent"])

	assert.Equal(t, "neutree/neutree-serve:v1.1.1", versions["v1.2.0"].Components["ray_runtime"])
	assert.Equal(t, "neutree/router:v1.1.1", versions["v1.2.0"].Components["router"])
	assert.Equal(t, "neutree/neutree-node-agent:v1.1.0-rc.1", versions["v1.2.0"].Components["node_agent"])

	for _, version := range versions {
		assert.Equal(t, v1.ReleaseInfoClusterVersionStateActive, version.State)
		assert.Equal(t, "quay.io/prometheus/node-exporter:v1.8.2", version.Components["node_exporter"])
		assert.Equal(t, "victoriametrics/vmagent:v1.115.0", version.Components["vmagent"])
		assert.Equal(t, "registry.k8s.io/kube-state-metrics/kube-state-metrics:v2.15.0", version.Components["kube_state_metrics"])
		assert.Equal(t, "nvcr.io/nvidia/k8s/dcgm-exporter:4.5.3-4.8.2-distroless", version.AcceleratorComponents["nvidia_gpu"]["dcgm_exporter"])
	}

	assert.Equal(t, "neutree/neutree-serve:v1.1.0-rocm", versions["v1.1.0"].AcceleratorComponents["amd_gpu"]["ray_runtime"])
	assert.Equal(t, "neutree/neutree-serve:v1.1.1-rocm", versions["v1.1.1"].AcceleratorComponents["amd_gpu"]["ray_runtime"])
	assert.Equal(t, "neutree/neutree-serve:v1.1.1-rocm", versions["v1.2.0"].AcceleratorComponents["amd_gpu"]["ray_runtime"])
}
