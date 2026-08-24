package metrics

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"gotest.tools/v3/assert"
)

func TestBuildExporterReadinessProbe(t *testing.T) {
	probe, err := buildExporterReadinessProbe(&v1.AcceleratorExporterReadiness{
		HTTPPath:            "/metrics",
		InitialDelaySeconds: 15,
		PeriodSeconds:       5,
		TimeoutSeconds:      5,
		FailureThreshold:    3,
	})

	assert.NilError(t, err)
	assert.Assert(t, probe != nil)
	assert.Assert(t, probe.HTTPGet != nil)
	assert.Equal(t, "/metrics", probe.HTTPGet.Path)
	assert.Equal(t, "metrics", probe.HTTPGet.Port.StrVal)
	assert.Equal(t, int32(15), probe.InitialDelaySeconds)
	assert.Equal(t, int32(5), probe.PeriodSeconds)
	assert.Equal(t, int32(5), probe.TimeoutSeconds)
	assert.Equal(t, int32(3), probe.FailureThreshold)
}

func TestBuildExporterReadinessProbeRejectsInvalidValues(t *testing.T) {
	valid := v1.AcceleratorExporterReadiness{
		HTTPPath:            "/metrics",
		InitialDelaySeconds: 15,
		PeriodSeconds:       5,
		TimeoutSeconds:      5,
		FailureThreshold:    3,
	}

	tests := []struct {
		name    string
		mutate  func(*v1.AcceleratorExporterReadiness)
		wantErr string
	}{
		{
			name: "empty path",
			mutate: func(readiness *v1.AcceleratorExporterReadiness) {
				readiness.HTTPPath = ""
			},
			wantErr: "readiness http_path",
		},
		{
			name: "relative path",
			mutate: func(readiness *v1.AcceleratorExporterReadiness) {
				readiness.HTTPPath = "metrics"
			},
			wantErr: "absolute clean path",
		},
		{
			name: "unclean path",
			mutate: func(readiness *v1.AcceleratorExporterReadiness) {
				readiness.HTTPPath = "/health/../metrics"
			},
			wantErr: "absolute clean path",
		},
		{
			name: "negative initial delay",
			mutate: func(readiness *v1.AcceleratorExporterReadiness) {
				readiness.InitialDelaySeconds = -1
			},
			wantErr: "initial_delay_seconds",
		},
		{
			name: "zero period",
			mutate: func(readiness *v1.AcceleratorExporterReadiness) {
				readiness.PeriodSeconds = 0
			},
			wantErr: "period_seconds",
		},
		{
			name: "zero timeout",
			mutate: func(readiness *v1.AcceleratorExporterReadiness) {
				readiness.TimeoutSeconds = 0
			},
			wantErr: "timeout_seconds",
		},
		{
			name: "zero failure threshold",
			mutate: func(readiness *v1.AcceleratorExporterReadiness) {
				readiness.FailureThreshold = 0
			},
			wantErr: "failure_threshold",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			readiness := valid
			tt.mutate(&readiness)

			_, err := buildExporterReadinessProbe(&readiness)
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestBuildComponentVolumesProjectsStructuredHostPaths(t *testing.T) {
	writable := false
	mounts, volumes, err := buildComponentVolumes(
		[]v1.ComponentVolume{
			{
				Name: "ascend-driver",
				HostPath: &v1.ComponentHostPathVolumeSource{
					Path: "/usr/local/Ascend/driver",
					Type: v1.ComponentHostPathTypeDirectory,
				},
			},
			{
				Name: "containerd-socket",
				HostPath: &v1.ComponentHostPathVolumeSource{
					Path: "/run/containerd/containerd.sock",
					Type: v1.ComponentHostPathTypeSocket,
				},
			},
		},
		[]v1.ComponentVolumeMount{
			{Name: "ascend-driver", MountPath: "/usr/local/Ascend/driver"},
			{Name: "containerd-socket", MountPath: "/run/containerd/containerd.sock", ReadOnly: &writable},
		},
	)

	assert.NilError(t, err)
	assert.DeepEqual(t, []corev1.VolumeMount{
		{Name: "ascend-driver", MountPath: "/usr/local/Ascend/driver", ReadOnly: true},
		{Name: "containerd-socket", MountPath: "/run/containerd/containerd.sock", ReadOnly: false},
	}, mounts)
	assert.Equal(t, 2, len(volumes))
	assert.Equal(t, "ascend-driver", volumes[0].Name)
	assert.Assert(t, volumes[0].HostPath != nil)
	assert.Equal(t, corev1.HostPathDirectory, *volumes[0].HostPath.Type)
	assert.Equal(t, "containerd-socket", volumes[1].Name)
	assert.Assert(t, volumes[1].HostPath != nil)
	assert.Equal(t, corev1.HostPathSocket, *volumes[1].HostPath.Type)
}

func TestBuildComponentVolumesRejectsInvalidProfiles(t *testing.T) {
	validVolumes := []v1.ComponentVolume{
		{
			Name: "ascend-driver",
			HostPath: &v1.ComponentHostPathVolumeSource{
				Path: "/usr/local/Ascend/driver",
				Type: v1.ComponentHostPathTypeDirectory,
			},
		},
	}
	validMounts := []v1.ComponentVolumeMount{
		{Name: "ascend-driver", MountPath: "/usr/local/Ascend/driver"},
	}

	tests := []struct {
		name    string
		volumes []v1.ComponentVolume
		mounts  []v1.ComponentVolumeMount
		wantErr string
	}{
		{
			name:    "missing host path",
			volumes: []v1.ComponentVolume{{Name: "ascend-driver"}},
			mounts:  validMounts,
			wantErr: "must declare host_path",
		},
		{
			name: "unsupported host path type",
			volumes: []v1.ComponentVolume{{
				Name: "ascend-driver",
				HostPath: &v1.ComponentHostPathVolumeSource{
					Path: "/usr/local/Ascend/driver",
					Type: "file",
				},
			}},
			mounts:  validMounts,
			wantErr: "is unsupported",
		},
		{
			name: "unclean host path",
			volumes: []v1.ComponentVolume{{
				Name: "ascend-driver",
				HostPath: &v1.ComponentHostPathVolumeSource{
					Path: "/usr/local/Ascend/driver/../driver",
					Type: v1.ComponentHostPathTypeDirectory,
				},
			}},
			mounts:  validMounts,
			wantErr: "absolute clean path",
		},
		{
			name: "duplicate volume name",
			volumes: append(validVolumes, v1.ComponentVolume{
				Name: "ascend-driver",
				HostPath: &v1.ComponentHostPathVolumeSource{
					Path: "/run/containerd",
					Type: v1.ComponentHostPathTypeDirectory,
				},
			}),
			mounts:  validMounts,
			wantErr: "must be unique",
		},
		{
			name:    "missing mount",
			volumes: validVolumes,
			wantErr: "must have exactly one mount",
		},
		{
			name:    "unknown mount volume",
			volumes: validVolumes,
			mounts:  []v1.ComponentVolumeMount{{Name: "unknown", MountPath: "/unknown"}},
			wantErr: "does not reference",
		},
		{
			name:    "container root mount",
			volumes: validVolumes,
			mounts:  []v1.ComponentVolumeMount{{Name: "ascend-driver", MountPath: "/"}},
			wantErr: "must not be the container root",
		},
		{
			name: "duplicate mount path",
			volumes: []v1.ComponentVolume{
				validVolumes[0],
				{
					Name: "containerd-socket",
					HostPath: &v1.ComponentHostPathVolumeSource{
						Path: "/run/containerd/containerd.sock",
						Type: v1.ComponentHostPathTypeSocket,
					},
				},
			},
			mounts: []v1.ComponentVolumeMount{
				{Name: "ascend-driver", MountPath: "/usr/local/Ascend/driver"},
				{Name: "containerd-socket", MountPath: "/usr/local/Ascend/driver"},
			},
			wantErr: "mount path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := buildComponentVolumes(tt.volumes, tt.mounts)
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestValidateExporterVolumeCollisions(t *testing.T) {
	tests := []struct {
		name           string
		runtimeMounts  []corev1.VolumeMount
		runtimeVolumes []corev1.Volume
		configMounts   []corev1.VolumeMount
		configVolumes  []corev1.Volume
		wantErr        string
	}{
		{
			name:           "volume name",
			runtimeVolumes: []corev1.Volume{{Name: "npu-exporter-config"}},
			configVolumes:  []corev1.Volume{{Name: "npu-exporter-config"}},
			wantErr:        "volume name",
		},
		{
			name:          "mount name",
			runtimeMounts: []corev1.VolumeMount{{Name: "npu-exporter-config", MountPath: "/driver"}},
			configMounts:  []corev1.VolumeMount{{Name: "npu-exporter-config", MountPath: "/config"}},
			wantErr:       "mount name",
		},
		{
			name:          "mount path",
			runtimeMounts: []corev1.VolumeMount{{Name: "driver", MountPath: "/config"}},
			configMounts:  []corev1.VolumeMount{{Name: "npu-exporter-config", MountPath: "/config"}},
			wantErr:       "mount path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateExporterVolumeCollisions(tt.runtimeMounts, tt.runtimeVolumes, tt.configMounts, tt.configVolumes)
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}
