package staticcluster

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlannerProjectsExplicitNodeAgentTargetWithoutExporterRuntimeInheritance(t *testing.T) {
	cluster := testStaticNodeCluster()
	currentNodes := []*v1.StaticNode{
		staticNodeStatusWithAccelerator(
			"head-0",
			v1.StaticNodeRoleHead,
			v1.StaticNodePhaseReady,
			true,
			v1.StaticNodeAcceleratorStatus{Type: "custom_accelerator"},
			nil,
		),
	}

	nodes := plannedStaticNodes(t, &Planner{
		AcceleratorProfileProvider: fakeAcceleratorProfileProvider{
			profiles: map[string]*v1.AcceleratorProfile{
				"custom_accelerator": {
					AcceleratorType:  "custom_accelerator",
					NodeAgentRuntime: &v1.NodeAgentRuntimeProfile{},
					MetricsExporter: &v1.AcceleratorExporterProfile{
						Name:        "custom-exporter",
						Image:       "example.com/custom/exporter:test",
						Backends:    []v1.AcceleratorExporterBackend{v1.AcceleratorExporterBackendStatic},
						Command:     []string{"/usr/local/bin/custom-exporter"},
						Port:        18082,
						MetricsPath: "custom/metrics",
						Env:         map[string]string{"EXPORTER_VISIBLE": "all"},
						Runtime: &v1.AcceleratorExporterRuntimeProfile{
							Capabilities:     &v1.AcceleratorExporterCapabilities{Add: []string{"SYS_ADMIN"}},
							DockerRunOptions: []string{"--gpus", "all"},
						},
					},
				},
			},
		},
	}, cluster, currentNodes)

	head := findStaticNode(nodes, "head-0")
	require.NotNil(t, head)
	nodeAgent := findComponent(head.Spec.Components, nodeAgentComponentName)
	require.NotNil(t, nodeAgent)
	exporter := findComponent(head.Spec.Components, acceleratorExporterComponentName)
	require.NotNil(t, exporter)

	assert.Equal(t, []string{"/usr/local/bin/custom-exporter"}, exporter.Command)
	assert.Equal(t, map[string]string{"EXPORTER_VISIBLE": "all"}, exporter.Env)
	assert.Contains(t, nodeAgent.Args, "--accelerator-type=custom_accelerator")
	assert.Contains(t, nodeAgent.Args, "--accelerator-exporter-port=18082")
	assert.Contains(t, nodeAgent.Args, "--accelerator-exporter-metrics-path=/custom/metrics")
	assert.Empty(t, nodeAgent.Env)
	assert.NotContains(t, nodeAgent.DockerRunOptions, "--cap-add=SYS_ADMIN")
	assert.NotContains(t, nodeAgent.DockerRunOptions, "--gpus")
}

func TestPlannerSkipsExporterOutsideStaticBackend(t *testing.T) {
	cluster := testStaticNodeCluster()
	currentNodes := []*v1.StaticNode{
		staticNodeStatusWithAccelerator(
			"head-0",
			v1.StaticNodeRoleHead,
			v1.StaticNodePhaseReady,
			true,
			v1.StaticNodeAcceleratorStatus{Type: "custom_accelerator"},
			nil,
		),
	}

	nodes := plannedStaticNodes(t, &Planner{
		AcceleratorProfileProvider: fakeAcceleratorProfileProvider{
			profiles: map[string]*v1.AcceleratorProfile{
				"custom_accelerator": {
					AcceleratorType: "custom_accelerator",
					MetricsExporter: &v1.AcceleratorExporterProfile{
						Name:     "custom-exporter",
						Image:    "example.com/custom/exporter:test",
						Port:     18082,
						Backends: []v1.AcceleratorExporterBackend{v1.AcceleratorExporterBackendKubernetes},
					},
				},
			},
		},
	}, cluster, currentNodes)

	head := findStaticNode(nodes, "head-0")
	require.NotNil(t, head)
	assert.Nil(t, findComponent(head.Spec.Components, acceleratorExporterComponentName))
}
