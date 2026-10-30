package version

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	kubeversion "k8s.io/apimachinery/pkg/version"

	"github.com/neutree-ai/neutree/internal/semver"
)

func TestKubeSchedulerVersionForClusterVersion(test *testing.T) {
	cases := []struct {
		name           string
		clusterVersion string
		serverVersion  *kubeversion.Info
		want           string
		wantSource     SchedulerVersionSource
	}{
		{
			name:           "legacy cluster keeps the pin at the Kubernetes 1.32 boundary",
			clusterVersion: "v1.2.1",
			serverVersion:  &kubeversion.Info{GitVersion: "v1.32.1"},
			want:           "v1.32.13",
			wantSource:     SchedulerVersionPinned,
		},
		{
			name:           "legacy cluster above Kubernetes 1.32 keeps the detected version",
			clusterVersion: "v1.2.1",
			serverVersion:  &kubeversion.Info{GitVersion: "v1.33.1"},
			want:           "v1.33.1",
			wantSource:     SchedulerVersionDetected,
		},
		{
			name:           "gate version pins the new Kubernetes minor",
			clusterVersion: "v1.3.0",
			serverVersion:  &kubeversion.Info{GitVersion: "v1.33.1"},
			want:           "v1.33.13",
			wantSource:     SchedulerVersionPinned,
		},
		{
			name:           "gate version pins the newest supported minor",
			clusterVersion: "v1.3.0",
			serverVersion:  &kubeversion.Info{GitVersion: "v1.35.2+k3s1"},
			want:           "v1.35.9",
			wantSource:     SchedulerVersionPinned,
		},
		{
			name:           "gate prerelease uses the new pin range",
			clusterVersion: "v1.3.0-rc.1",
			serverVersion:  &kubeversion.Info{GitVersion: "v1.33.1"},
			want:           "v1.33.13",
			wantSource:     SchedulerVersionPinned,
		},
		{
			name:           "minor without a pin keeps the detected version",
			clusterVersion: "v1.3.0",
			serverVersion:  &kubeversion.Info{GitVersion: "v1.36.0"},
			want:           "v1.36.0",
			wantSource:     SchedulerVersionDetected,
		},
	}

	for _, testCase := range cases {
		test.Run(testCase.name, func(test *testing.T) {
			schedulerVersion, source := KubeSchedulerVersionForClusterVersion(
				testCase.clusterVersion, testCase.serverVersion)

			assert.Equal(test, testCase.want, schedulerVersion)
			assert.Equal(test, testCase.wantSource, source)
		})
	}
}

func TestKubeSchedulerVersionsByMinorMatchPinnedVersion(test *testing.T) {
	for minor, pinnedVersion := range KubeSchedulerVersionsByMinor {
		test.Run(minor, func(test *testing.T) {
			require.NotEmpty(test, pinnedVersion)

			majorMinor, err := semver.MajorMinor(pinnedVersion)
			require.NoError(test, err, "pinned scheduler version %q must be semver", pinnedVersion)
			assert.Equal(test, minor, majorMinor,
				"scheduler pin %q must belong to Kubernetes minor %s", pinnedVersion, minor)
		})
	}
}

func TestKubeVersionFromBaseVersionFallsBackToDefaultMinor(test *testing.T) {
	version := KubeVersionFromBaseVersion("not-a-version")

	assert.Equal(test, "1", version.Major)
	assert.Equal(test, "32", version.Minor)
}
