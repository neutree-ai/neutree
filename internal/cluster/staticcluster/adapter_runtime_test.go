package staticcluster

import (
	"context"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildNodeAgentComponentProjectsAdapterRuntimeAccess(t *testing.T) {
	readOnly := true
	profile := &v1.AcceleratorProfile{
		AcceleratorType: "vendor_accelerator",
		MetricsExporter: &v1.AcceleratorExporterProfile{
			Name:    "vendor-exporter",
			Image:   "registry.example.com/vendor-exporter:test",
			Command: []string{"/usr/local/bin/vendor-exporter"},
			Args:    []string{"--listen=8082"},
			Port:    8082,
			Readiness: &v1.AcceleratorExporterReadiness{
				HTTPPath: "/readyz",
			},
			Env: map[string]string{
				v1.NodeAgentAdapterProfileKey: "true",
			},
			ConfigFiles: []v1.AcceleratorExporterConfigFile{{
				Path: "/etc/vendor/exporter.yaml",
			}},
			Runtime: &v1.AcceleratorExporterRuntimeProfile{
				Privileged: true,
				Runtime:    "vendor-runtime",
				Capabilities: &v1.AcceleratorExporterCapabilities{
					Add: []string{"SYS_ADMIN"},
				},
				Volumes: []v1.ComponentVolume{{
					Name: "accelerator-driver",
					HostPath: &v1.ComponentHostPathVolumeSource{
						Path: "/opt/vendor/driver",
						Type: v1.ComponentHostPathTypeDirectory,
					},
				}},
				VolumeMounts: []v1.ComponentVolumeMount{{
					Name:      "accelerator-driver",
					MountPath: "/opt/vendor/driver",
					ReadOnly:  &readOnly,
				}},
			},
		},
	}
	node := &v1.StaticNode{
		Metadata: &v1.Metadata{Name: "accelerator-0"},
		Spec:     &v1.StaticNodeSpec{IP: "10.0.0.20"},
	}

	component := buildNodeAgentComponent(testStaticNodeCluster(), node, profile)

	assert.Contains(t, component.Args, "--accelerator-type=vendor_accelerator")
	assert.Contains(t, component.Args, "--accelerator-exporter-port=8082")
	assert.Contains(t, component.Args, "--accelerator-exporter-metrics-path=/metrics")
	assert.Contains(t, component.DockerRunOptions, "--privileged")
	assert.Contains(t, component.DockerRunOptions, "--cap-add=SYS_ADMIN")
	assert.Contains(t, component.DockerRunOptions, "--runtime=vendor-runtime")
	assert.NotContains(t, component.Args, "--listen=8082")
	assert.Empty(t, component.Command)
	assert.Empty(t, component.ConfigFiles)
	requireVolume(t, &component, "accelerator-driver", "/opt/vendor/driver", "/opt/vendor/driver")
}

func TestBuildAcceleratorExporterComponentUsesStructuredRuntimeProfile(t *testing.T) {
	readOnly := true
	exporter := buildAcceleratorExporterComponent(testStaticNodeCluster(), &v1.AcceleratorExporterProfile{
		Name:    "vendor-exporter",
		Image:   "registry.example.com/vendor-exporter:test",
		Command: []string{"/usr/local/bin/vendor-exporter"},
		Args:    []string{"--listen=8082"},
		Port:    8082,
		Readiness: &v1.AcceleratorExporterReadiness{
			HTTPPath:            "/readyz",
			InitialDelaySeconds: 3,
			PeriodSeconds:       5,
			TimeoutSeconds:      2,
		},
		Runtime: &v1.AcceleratorExporterRuntimeProfile{
			Privileged: true,
			Runtime:    "vendor-runtime",
			Volumes: []v1.ComponentVolume{{
				Name: "accelerator-driver",
				HostPath: &v1.ComponentHostPathVolumeSource{
					Path: "/opt/vendor/driver",
					Type: v1.ComponentHostPathTypeDirectory,
				},
			}},
			VolumeMounts: []v1.ComponentVolumeMount{{
				Name:      "accelerator-driver",
				MountPath: "/opt/vendor/driver",
				ReadOnly:  &readOnly,
			}},
		},
	})

	assert.Equal(t, []string{"/usr/local/bin/vendor-exporter"}, exporter.Command)
	assert.Equal(t, []string{"--listen=8082"}, exporter.Args)
	assert.Contains(t, exporter.DockerRunOptions, "--privileged")
	assert.Contains(t, exporter.DockerRunOptions, "--runtime=vendor-runtime")
	requireVolume(t, &exporter, "accelerator-driver", "/opt/vendor/driver", "/opt/vendor/driver")
	require.NotNil(t, exporter.HealthCheck)
	assert.Equal(t, "/readyz", exporter.HealthCheck.HTTPPath)
	assert.Equal(t, 3, exporter.HealthCheck.InitialDelaySec)
	assert.Equal(t, 5, exporter.HealthCheck.IntervalSec)
	assert.Equal(t, 2, exporter.HealthCheck.TimeoutSec)
}

func TestAcceleratorExporterTargetGroupsUseMetricsPathRatherThanReadinessPath(t *testing.T) {
	exporter := buildAcceleratorExporterComponent(testStaticNodeCluster(), &v1.AcceleratorExporterProfile{
		Name:        "vendor-exporter",
		Image:       "registry.example.com/vendor-exporter:test",
		Port:        8082,
		MetricsPath: "/metrics",
		Readiness: &v1.AcceleratorExporterReadiness{
			HTTPPath: "/readyz",
		},
	})

	groups := acceleratorExporterTargetGroups(testStaticNodeCluster(), []DesiredNodePlan{{
		Node: &v1.StaticNode{
			Metadata: &v1.Metadata{Name: "accelerator-0"},
			Spec:     &v1.StaticNodeSpec{IP: "10.0.0.20", Components: []v1.NodeComponentSpec{exporter}},
		},
		Accelerator:                    &v1.StaticNodeAcceleratorStatus{Type: "vendor_accelerator"},
		AcceleratorExporterMetricsPath: "/metrics",
	}})

	require.Len(t, groups, 1)
	assert.Equal(t, "/metrics", groups[0].MetricsPath)
	require.NotNil(t, exporter.HealthCheck)
	assert.Equal(t, "/readyz", exporter.HealthCheck.HTTPPath)
}

func TestValidateStaticRuntimeAccessRejectsUnpairedVolumeMount(t *testing.T) {
	profile := &v1.AcceleratorProfile{
		MetricsExporter: &v1.AcceleratorExporterProfile{
			Name:  "vendor-exporter",
			Image: "registry.example.com/vendor-exporter:test",
			Port:  8082,
			Runtime: &v1.AcceleratorExporterRuntimeProfile{
				Volumes: []v1.ComponentVolume{{
					Name: "driver",
					HostPath: &v1.ComponentHostPathVolumeSource{
						Path: "/opt/vendor/driver",
						Type: v1.ComponentHostPathTypeDirectory,
					},
				}},
				VolumeMounts: []v1.ComponentVolumeMount{{
					Name:      "missing",
					MountPath: "/opt/vendor/driver",
				}},
			},
		},
	}

	assert.ErrorContains(t, validateStaticRuntimeAccess(profile), "does not reference a declared runtime volume")
}

func TestValidateStaticRuntimeAccessRejectsHiddenDeviceAndMountOptions(t *testing.T) {
	for _, option := range []string{
		"--device=/dev/vendor0",
		"--volume=/opt/vendor:/opt/vendor:ro",
		"--mount type=bind,src=/opt/vendor,dst=/opt/vendor",
		"-v/opt/vendor:/opt/vendor:ro",
	} {
		t.Run(option, func(t *testing.T) {
			profile := &v1.AcceleratorProfile{
				MetricsExporter: &v1.AcceleratorExporterProfile{
					Name:  "vendor-exporter",
					Image: "registry.example.com/vendor-exporter:test",
					Port:  8082,
					Runtime: &v1.AcceleratorExporterRuntimeProfile{
						DockerRunOptions: []string{option},
					},
				},
			}

			assert.ErrorContains(t, validateStaticRuntimeAccess(profile), "must use structured runtime volumes")
		})
	}
}

func TestPlannerRejectsInvalidStaticRuntimeAccess(t *testing.T) {
	cluster := testStaticNodeCluster()
	planner := &Planner{
		AcceleratorProfileProvider: fakeAcceleratorProfileProvider{profiles: map[string]*v1.AcceleratorProfile{
			v1.AcceleratorTypeNVIDIAGPU.String(): {
				AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.String(),
				MetricsExporter: &v1.AcceleratorExporterProfile{
					Name:  "vendor-exporter",
					Image: "registry.example.com/vendor-exporter:test",
					Port:  8082,
					Runtime: &v1.AcceleratorExporterRuntimeProfile{
						Volumes: []v1.ComponentVolume{{
							Name: "unmounted-driver",
							HostPath: &v1.ComponentHostPathVolumeSource{
								Path: "/opt/vendor/driver",
								Type: v1.ComponentHostPathTypeDirectory,
							},
						}},
					},
				},
			},
		}},
	}

	_, err := planner.Plan(context.Background(), cluster, []*v1.StaticNode{
		staticNodeStatusWithAccelerator(
			"head-0",
			v1.StaticNodeRoleHead,
			v1.StaticNodePhaseReady,
			true,
			nvidiaAcceleratorStatus(),
			nil,
		),
	})

	assert.ErrorContains(t, err, "validate static accelerator runtime profile for nvidia_gpu")
	assert.ErrorContains(t, err, "runtime volume \"unmounted-driver\" must have one mount")
}
