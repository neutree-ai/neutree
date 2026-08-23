package releaseprofile

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestValidateReleaseInfo(t *testing.T) {
	tests := []struct {
		name string
		info *v1.ReleaseInfo
		want string
	}{
		{
			name: "valid prerelease identity",
			info: releaseInfoForTest("v1.2.0-alpha.1", "v1.2.0", []string{"v1.1", "v1.2"}),
		},
		{
			name: "missing default",
			info: releaseInfoForTest("v1.2.0", "", []string{"v1.2"}),
			want: "default cluster version is required",
		},
		{
			name: "non minor baseline",
			info: releaseInfoForTest("v1.2.0", "v1.2.0", []string{"v1.2.0"}),
			want: "invalid compatible cluster baseline",
		},
		{
			name: "duplicate baseline",
			info: releaseInfoForTest("v1.2.0", "v1.2.0", []string{"v1.2", "v1.2"}),
			want: "duplicate compatible cluster baseline",
		},
		{
			name: "default minor excluded",
			info: releaseInfoForTest("v1.2.0", "v1.2.0", []string{"v1.1"}),
			want: "default cluster version",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateReleaseInfo(tt.info)
			if tt.want == "" {
				require.NoError(t, err)
				return
			}

			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestValidateClusterProfile(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*v1.ClusterProfile)
		wantErr string
	}{
		{
			name: "valid complete profile",
		},
		{
			name: "extra cluster type",
			mutate: func(profile *v1.ClusterProfile) {
				profile.Spec.Components["docker"] = v1.ClusterProfileComponents{}
			},
			wantErr: "unsupported component matrix type",
		},
		{
			name: "missing kubernetes matrix",
			mutate: func(profile *v1.ClusterProfile) {
				delete(profile.Spec.Components, v1.KubernetesClusterType)
			},
			wantErr: "kubernetes component matrix is required",
		},
		{
			name: "missing image tag",
			mutate: func(profile *v1.ClusterProfile) {
				ssh := profile.Spec.Components[v1.SSHClusterType]
				ssh.RayRuntime.Tag = ""
				profile.Spec.Components[v1.SSHClusterType] = ssh
			},
			wantErr: "ray runtime tag is required",
		},
		{
			name: "workspace is forbidden",
			mutate: func(profile *v1.ClusterProfile) {
				profile.Metadata.Workspace = "default"
			},
			wantErr: "metadata.workspace must be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile := completeProfileForTest("v1.2.0")
			if tt.mutate != nil {
				tt.mutate(profile)
			}

			err := ValidateClusterProfile(profile)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}

			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestValidateProfileEligibility(t *testing.T) {
	info := releaseInfoForTest("v1.2.0", "v1.2.0", []string{"v1.1", "v1.2"})

	tests := []struct {
		name    string
		version string
		wantErr string
	}{
		{name: "compatible historical patch", version: "v1.1.1"},
		{name: "compatible prerelease", version: "v1.2.0-alpha.1"},
		{name: "version above default", version: "v1.2.1", wantErr: "exceeds default cluster version"},
		{name: "incompatible minor", version: "v1.0.1", wantErr: "incompatible baseline"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateProfileEligibility(info, completeProfileForTest(tt.version))
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}

			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestClusterProfilesSemanticallyEqualIgnoresServerManagedFields(t *testing.T) {
	left := completeProfileForTest("v1.2.0")
	left.ID = 1
	left.Metadata.CreationTimestamp = "2026-08-01T00:00:00Z"

	right := completeProfileForTest("v1.2.0")
	right.ID = 2
	right.Metadata.CreationTimestamp = "2026-08-02T00:00:00Z"
	right.Metadata.Labels = map[string]string{}
	right.Metadata.Annotations = map[string]string{}

	assert.True(t, ClusterProfilesSemanticallyEqual(left, right))

	ssh := right.Spec.Components[v1.SSHClusterType]
	ssh.RayRuntime.Tag = "v1.2.0-drift"
	right.Spec.Components[v1.SSHClusterType] = ssh
	assert.False(t, ClusterProfilesSemanticallyEqual(left, right))
}

func releaseInfoForTest(name, defaultClusterVersion string, baselines []string) *v1.ReleaseInfo {
	return &v1.ReleaseInfo{
		APIVersion: "v1",
		Kind:       v1.ReleaseInfoKind,
		Metadata:   &v1.Metadata{Name: name},
		Spec: &v1.ReleaseInfoSpec{
			DefaultClusterVersion:      defaultClusterVersion,
			CompatibleClusterBaselines: append([]string(nil), baselines...),
		},
	}
}

func completeProfileForTest(version string) *v1.ClusterProfile {
	return &v1.ClusterProfile{
		APIVersion: "v1",
		Kind:       v1.ClusterProfileKind,
		Metadata:   &v1.Metadata{Name: version},
		Spec: &v1.ClusterProfileSpec{Components: map[string]v1.ClusterProfileComponents{
			v1.SSHClusterType: {
				RayRuntime:   v1.ImageRef{Image: "neutree/neutree-serve", Tag: "v1.2.0"},
				NodeAgent:    v1.ImageRef{Image: "neutree/neutree-node-agent", Tag: "v1.2.0"},
				NodeExporter: v1.ImageRef{Image: "quay.io/prometheus/node-exporter", Tag: "v1.8.2"},
				VMAgent:      v1.ImageRef{Image: "victoriametrics/vmagent", Tag: "v1.115.0"},
			},
			v1.KubernetesClusterType: {
				KubernetesRuntime: v1.ImageRef{Image: "neutree/neutree-runtime", Tag: "v1.2.0"},
				Router:            v1.ImageRef{Image: "neutree/router", Tag: "v1.2.0"},
				NodeAgent:         v1.ImageRef{Image: "neutree/neutree-node-agent", Tag: "v1.2.0"},
				NodeExporter:      v1.ImageRef{Image: "quay.io/prometheus/node-exporter", Tag: "v1.8.2"},
				VMAgent:           v1.ImageRef{Image: "victoriametrics/vmagent", Tag: "v1.115.0"},
				KubeStateMetrics:  v1.ImageRef{Image: "registry.k8s.io/kube-state-metrics/kube-state-metrics", Tag: "v2.15.0"},
			},
		}},
	}
}
