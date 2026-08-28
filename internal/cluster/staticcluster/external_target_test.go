package staticcluster

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestPlannerUsesExternalProfileTarget(t *testing.T) {
	cluster := testStaticNodeCluster()
	cluster.Spec.Metrics = &v1.ClusterMetricsConfig{
		AcceleratorExporter: &v1.ClusterAcceleratorExporterConfig{
			Mode: v1.ClusterAcceleratorExporterModeExternal,
		},
	}
	currentNodes := []*v1.StaticNode{
		staticNodeStatusWithAccelerator(
			"head-0",
			v1.StaticNodeRoleHead,
			v1.StaticNodePhaseReady,
			true,
			nvidiaAcceleratorStatus(),
			nil,
		),
	}

	nodes := plannedStaticNodes(t, &Planner{
		AcceleratorProfileProvider: fakeAcceleratorProfileProvider{
			profiles: map[string]*v1.AcceleratorProfile{
				v1.AcceleratorTypeNVIDIAGPU.String(): {
					AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.String(),
					MetricsExporter: &v1.AcceleratorExporterProfile{
						Name:  "managed-exporter",
						Image: "example.com/managed-exporter:v1",
						Port:  19400,
					},
					ExternalMetricsTarget: &v1.MetricsTargetProfile{
						Namespace:   "monitoring",
						PodSelector: map[string]string{"app": "external-exporter"},
						Port:        9400,
						MetricsPath: "/custom/metrics",
					},
				},
			},
		},
		MetricsRemoteWriteURL: "http://vm:8480/insert/0/prometheus/",
	}, cluster, currentNodes)

	head := findStaticNode(nodes, "head-0")
	require.NotNil(t, head)
	assert.Nil(t, findComponent(head.Spec.Components, acceleratorExporterComponentName))

	nodeAgent := findComponent(head.Spec.Components, nodeAgentComponentName)
	require.NotNil(t, nodeAgent)
	assert.Contains(t, nodeAgent.Args, "--accelerator-exporter-port=9400")
	assert.Contains(t, nodeAgent.Args, "--accelerator-exporter-metrics-path=/custom/metrics")

	vmagent := findComponent(head.Spec.Components, vmagentComponentName)
	require.NotNil(t, vmagent)
	config := findConfigFile(vmagent.ConfigFiles, "/etc/neutree/vmagent/file_sd/accelerator-exporter-nvidia-gpu.json")
	require.NotNil(t, config)
	assert.Contains(t, config.Content, `"10.0.0.10:9400"`)
	assert.Contains(t, findConfigFile(vmagent.ConfigFiles, "/etc/neutree/vmagent/config.yaml").Content, `metrics_path: "/custom/metrics"`)
}
