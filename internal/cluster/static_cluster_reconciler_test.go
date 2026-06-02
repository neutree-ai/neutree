package cluster

import (
	"encoding/json"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStaticClusterReconcilerBuildDesiredNodes(t *testing.T) {
	cluster := testStaticCluster()
	profiles := map[string]*v1.AcceleratorProfile{
		v1.AcceleratorTypeNVIDIAGPU.String(): {
			AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.String(),
			Metrics: &v1.AcceleratorMetricsProfile{
				Exporter: &v1.AcceleratorExporterProfile{
					Kind:             "dcgm-exporter",
					WorkerType:       v1.NodeWorkerTypeAcceleratorExporter,
					Image:            "nvcr.io/nvidia/k8s/dcgm-exporter:test",
					Port:             9400,
					DockerRunOptions: []string{"--gpus all", "--cap-add=SYS_ADMIN"},
				},
			},
		},
	}

	nodes, err := (&StaticClusterReconciler{}).BuildDesiredNodes(cluster, profiles)

	require.NoError(t, err)
	require.Len(t, nodes, 2)

	head := nodes[0]
	require.NotNil(t, head.Metadata)
	require.NotNil(t, head.Spec)
	assert.Equal(t, "head-0", head.Metadata.Name)
	assert.Equal(t, "default", head.Metadata.Workspace)
	assert.Equal(t, "static-a", head.Spec.Cluster)
	assert.Equal(t, v1.StaticNodeRoleHead, head.Spec.Role)
	assert.Equal(t, "10.0.0.10", head.Spec.IP)
	assert.Equal(t, "ssh-ref", head.Spec.SSHAuthRef)
	assert.Equal(t, map[string]string{
		staticClusterLabelKey:  "static-a",
		staticNodeRoleLabelKey: string(v1.StaticNodeRoleHead),
	}, head.Metadata.Labels)
	require.NotNil(t, head.Spec.Warm)
	assert.Equal(t, "registry.example.com/neutree/serve:v1.2.0", head.Spec.Warm.Images[0].Ref)
	assertNodeWorkerTypes(t, head.Spec.Workers, []v1.NodeWorkerType{
		v1.NodeWorkerTypeRayHead,
		v1.NodeWorkerTypeNodeExporter,
		v1.NodeWorkerTypeAcceleratorExporter,
		v1.NodeWorkerTypeMetricsNormalizer,
		v1.NodeWorkerTypeMetricsAgent,
	})
	exporter := findWorker(head.Spec.Workers, acceleratorExporterName)
	require.NotNil(t, exporter)
	assert.Equal(t, "nvcr.io/nvidia/k8s/dcgm-exporter:test", exporter.Image)
	assert.Equal(t, []string{"--gpus all", "--cap-add=SYS_ADMIN"}, exporter.DockerRunOptions)
	assert.Equal(t, 9400, exporter.Ports[0].Port)

	metricsWorker := findWorker(head.Spec.Workers, neutreeMetricsWorkerName)
	require.NotNil(t, metricsWorker)
	assert.NotEmpty(t, metricsWorker.ConfigHash)
	metricsConfig := findConfigFile(metricsWorker.ConfigFiles, neutreeMetricsConfigPath)
	require.NotNil(t, metricsConfig)
	assert.True(t, metricsConfig.Sudo)
	assert.True(t, metricsConfig.Atomic)
	assert.True(t, metricsConfig.CreateParent)
	var parsedMetricsConfig metricsNormalizerConfig
	require.NoError(t, json.Unmarshal([]byte(metricsConfig.Content), &parsedMetricsConfig))
	assert.Equal(t, "default", parsedMetricsConfig.Labels["workspace"])
	assert.Equal(t, "static-a", parsedMetricsConfig.Labels["static_cluster"])
	assert.Equal(t, "head-0", parsedMetricsConfig.Labels["node"])
	assert.Equal(t, "nvidia_gpu", parsedMetricsConfig.AcceleratorType)
	assert.Equal(t, "dcgm-exporter", parsedMetricsConfig.ExporterKind)
	require.Len(t, parsedMetricsConfig.Targets, 2)
	assert.Equal(t, "http://127.0.0.1:9100/metrics", parsedMetricsConfig.Targets[0].URL)
	assert.Equal(t, "http://127.0.0.1:9400/metrics", parsedMetricsConfig.Targets[1].URL)

	vmagentWorker := findWorker(head.Spec.Workers, vmagentWorkerName)
	require.NotNil(t, vmagentWorker)
	assert.NotEmpty(t, vmagentWorker.ConfigHash)
	vmagentConfig := findConfigFile(vmagentWorker.ConfigFiles, vmagentConfigPath)
	require.NotNil(t, vmagentConfig)
	assert.Contains(t, vmagentConfig.Content, `"10.0.0.10:19090"`)
	assert.Contains(t, vmagentConfig.Content, `"10.0.0.11:19090"`)
	assert.Contains(t, vmagentConfig.Content, `remote_write:`)
	assert.Contains(t, vmagentConfig.Content, `"http://vm:8480/insert/0/prometheus/"`)

	worker := nodes[1]
	require.NotNil(t, worker.Metadata)
	require.NotNil(t, worker.Spec)
	assert.Equal(t, "worker-0", worker.Metadata.Name)
	assert.Equal(t, v1.StaticNodeRoleWorker, worker.Spec.Role)
	assertNodeWorkerTypes(t, worker.Spec.Workers, []v1.NodeWorkerType{
		v1.NodeWorkerTypeRayWorker,
		v1.NodeWorkerTypeNodeExporter,
		v1.NodeWorkerTypeMetricsNormalizer,
	})

	cluster.Spec.Warm.Images[0].Ref = "mutated"
	assert.Equal(t, "registry.example.com/neutree/serve:v1.2.0", head.Spec.Warm.Images[0].Ref)
}

func TestStaticClusterReconcilerBuildDesiredNodesValidation(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*v1.StaticCluster)
		wantErr string
	}{
		{
			name: "missing head node",
			mutate: func(cluster *v1.StaticCluster) {
				cluster.Spec.Head.NodeName = "missing"
			},
			wantErr: "head node missing not found",
		},
		{
			name: "duplicate node",
			mutate: func(cluster *v1.StaticCluster) {
				cluster.Spec.Nodes[0].Name = "head-0"
			},
			wantErr: "duplicate static node head-0",
		},
		{
			name: "missing ip",
			mutate: func(cluster *v1.StaticCluster) {
				cluster.Spec.Nodes[0].IP = ""
			},
			wantErr: "static node worker-0 ip is required",
		},
		{
			name: "missing nodes",
			mutate: func(cluster *v1.StaticCluster) {
				cluster.Spec.Nodes = nil
			},
			wantErr: "static cluster spec.nodes is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := testStaticCluster()
			tt.mutate(cluster)

			_, err := (&StaticClusterReconciler{}).BuildDesiredNodes(cluster, nil)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestStaticClusterReconcilerAggregateStatus(t *testing.T) {
	tests := []struct {
		name       string
		nodes      []*v1.StaticNode
		wantStatus v1.StaticClusterStatus
	}{
		{
			name: "ready when all nodes, warm, and metrics are ready",
			nodes: []*v1.StaticNode{
				staticNodeStatus("head-0", v1.StaticNodeRoleHead, v1.StaticNodePhaseReady, true, []v1.NodeWorkerStatus{
					readyWorker(nodeExporterWorkerName),
					readyWorker(neutreeMetricsWorkerName),
					readyWorker(vmagentWorkerName),
				}),
				staticNodeStatus("worker-0", v1.StaticNodeRoleWorker, v1.StaticNodePhaseReady, true, []v1.NodeWorkerStatus{
					readyWorker(nodeExporterWorkerName),
					readyWorker(neutreeMetricsWorkerName),
				}),
			},
			wantStatus: v1.StaticClusterStatus{
				Phase:        v1.StaticClusterPhaseReady,
				DesiredNodes: 2,
				ReadyNodes:   2,
				HeadReady:    true,
				MetricsReady: true,
				WarmReady:    true,
			},
		},
		{
			name: "degraded when head is ready but a worker is not ready",
			nodes: []*v1.StaticNode{
				staticNodeStatus("head-0", v1.StaticNodeRoleHead, v1.StaticNodePhaseReady, true, []v1.NodeWorkerStatus{
					readyWorker(nodeExporterWorkerName),
					readyWorker(neutreeMetricsWorkerName),
					readyWorker(vmagentWorkerName),
				}),
				staticNodeStatus("worker-0", v1.StaticNodeRoleWorker, v1.StaticNodePhaseReconciling, false, nil),
			},
			wantStatus: v1.StaticClusterStatus{
				Phase:        v1.StaticClusterPhaseDegraded,
				DesiredNodes: 2,
				ReadyNodes:   1,
				HeadReady:    true,
				MetricsReady: false,
				WarmReady:    false,
			},
		},
		{
			name: "failed when any node failed",
			nodes: []*v1.StaticNode{
				staticNodeStatus("head-0", v1.StaticNodeRoleHead, v1.StaticNodePhaseFailed, false, nil),
			},
			wantStatus: v1.StaticClusterStatus{
				Phase:        v1.StaticClusterPhaseFailed,
				DesiredNodes: 2,
				ReadyNodes:   0,
				HeadReady:    false,
				MetricsReady: false,
				WarmReady:    false,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := (&StaticClusterReconciler{}).AggregateStatus(testStaticCluster(), tt.nodes)

			assert.Equal(t, tt.wantStatus, status)
		})
	}
}

func testStaticCluster() *v1.StaticCluster {
	return &v1.StaticCluster{
		Metadata: &v1.Metadata{
			Workspace:   "default",
			Name:        "static-a",
			Annotations: map[string]string{"source": "unit-test"},
		},
		Spec: &v1.StaticClusterSpec{
			Version: "v1.2.0",
			Head: v1.StaticClusterHeadSpec{
				NodeName: "head-0",
			},
			Nodes: []v1.StaticClusterNodeSpec{
				{
					Name:            "worker-0",
					IP:              "10.0.0.11",
					Role:            v1.StaticNodeRoleWorker,
					SSHAuthRef:      "ssh-ref",
					AcceleratorType: "",
				},
				{
					Name:            "head-0",
					IP:              "10.0.0.10",
					Role:            v1.StaticNodeRoleWorker,
					SSHAuthRef:      "ssh-ref",
					AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.String(),
				},
			},
			Warm: &v1.WarmSpec{
				Images: []v1.WarmImageSpec{
					{
						Name:     "ray-runtime",
						Ref:      "registry.example.com/neutree/serve:v1.2.0",
						Required: true,
					},
				},
			},
			MetricsRemoteWriteURL: "http://vm:8480/insert/0/prometheus/",
		},
	}
}

func staticNodeStatus(
	name string,
	role v1.StaticNodeRole,
	phase v1.StaticNodePhase,
	warmReady bool,
	workers []v1.NodeWorkerStatus,
) *v1.StaticNode {
	return &v1.StaticNode{
		Metadata: &v1.Metadata{Name: name},
		Spec:     &v1.StaticNodeSpec{Role: role},
		Status: &v1.StaticNodeStatus{
			Phase:   phase,
			Warm:    &v1.WarmStatus{Ready: warmReady},
			Workers: workers,
		},
	}
}

func readyWorker(name string) v1.NodeWorkerStatus {
	return v1.NodeWorkerStatus{
		Name:  name,
		Ready: true,
		Phase: v1.NodeWorkerPhaseRunning,
	}
}

func assertNodeWorkerTypes(t *testing.T, workers []v1.NodeWorkerSpec, want []v1.NodeWorkerType) {
	t.Helper()

	require.Len(t, workers, len(want))
	for i, worker := range workers {
		assert.Equal(t, want[i], worker.Type)
	}
}

func findWorker(workers []v1.NodeWorkerSpec, name string) *v1.NodeWorkerSpec {
	for i := range workers {
		if workers[i].Name == name {
			return &workers[i]
		}
	}

	return nil
}

func findConfigFile(configFiles []v1.NodeWorkerConfigFile, path string) *v1.NodeWorkerConfigFile {
	for i := range configFiles {
		if configFiles[i].Path == path {
			return &configFiles[i]
		}
	}

	return nil
}
