package metrics

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
)

func TestBuildComponentVolumesProjectsTrustedProfileWithoutValidation(t *testing.T) {
	readOnly := false
	mounts, volumes := buildComponentVolumes(
		[]v1.ComponentVolume{
			{
				Name:     "driver",
				HostPath: &v1.ComponentHostPathVolumeSource{Path: "relative-path", Type: "opaque"},
			},
			{Name: "missing-source"},
		},
		[]v1.ComponentVolumeMount{
			{Name: "driver", MountPath: "relative-mount", ReadOnly: &readOnly},
			{Name: "undeclared", MountPath: "/undeclared"},
		},
	)

	require.Len(t, volumes, 2)
	require.NotNil(t, volumes[0].HostPath)
	assert.Equal(t, "relative-path", volumes[0].HostPath.Path)
	require.NotNil(t, volumes[0].HostPath.Type)
	assert.Equal(t, corev1.HostPathType("opaque"), *volumes[0].HostPath.Type)
	assert.Nil(t, volumes[1].HostPath)
	require.Len(t, mounts, 2)
	assert.Equal(t, "relative-mount", mounts[0].MountPath)
	assert.False(t, mounts[0].ReadOnly)
	assert.Equal(t, "undeclared", mounts[1].Name)
	assert.True(t, mounts[1].ReadOnly)
}

func TestBuildComponentVolumesWithoutProfileEntries(t *testing.T) {
	mounts, volumes := buildComponentVolumes(nil, nil)

	assert.Nil(t, mounts)
	assert.Nil(t, volumes)
}
