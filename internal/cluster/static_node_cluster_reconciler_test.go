package cluster

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStaticNodeClusterReconcilerBuildDesiredNodes(t *testing.T) {
	cluster := testStaticNodeCluster()
	profiles := map[string]*v1.AcceleratorProfile{
		v1.AcceleratorTypeNVIDIAGPU.String(): {
			AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.String(),
			ClusterRuntime: &v1.RuntimeConfig{
				Runtime: "nvidia",
				Env: map[string]string{
					"ACCELERATOR_TYPE": "gpu",
				},
				Options: []string{"--gpus all"},
			},
			Metrics: &v1.AcceleratorMetricsProfile{
				Exporter: &v1.AcceleratorExporterProfile{
					Kind:             "dcgm-exporter",
					ComponentType:    v1.NodeComponentTypeAcceleratorExporter,
					Image:            "nvcr.io/nvidia/k8s/dcgm-exporter:test",
					Port:             9400,
					DockerRunOptions: []string{"--gpus all", "--cap-add=SYS_ADMIN"},
				},
			},
		},
	}

	nodes, err := (&StaticNodeClusterReconciler{}).BuildDesiredNodes(cluster, profiles)

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
	require.NotNil(t, head.Spec.SSHAuth)
	assert.Equal(t, "ray", head.Spec.SSHAuth.SSHUser)
	assert.Equal(t, map[string]string{
		staticNodeClusterLabelKey: "static-a",
		staticNodeRoleLabelKey:    string(v1.StaticNodeRoleHead),
	}, head.Metadata.Labels)
	rayHead := findComponent(head.Spec.Components, "ray-head")
	require.NotNil(t, rayHead)
	assert.Equal(t, "registry.example.com/neutree/neutree-serve:v1.2.0", rayHead.Image)
	assert.Equal(t, []string{"/bin/bash", "-lc"}, rayHead.Command)
	require.Len(t, rayHead.Args, 1)
	assert.Contains(t, rayHead.Args[0], "python /home/ray/start.py --head")
	assert.Contains(t, rayHead.Args[0], "--dashboard-port=8265")
	assert.Contains(t, rayHead.Args[0], v1.NeutreeServingVersionLabel)
	assert.NotContains(t, rayHead.Args[0], "--autoscaling-config")
	assert.Equal(t, "gpu", rayHead.Env["ACCELERATOR_TYPE"])
	assert.Contains(t, rayHead.DockerRunOptions, "--runtime=nvidia")
	assert.Contains(t, rayHead.DockerRunOptions, "--gpus all")
	require.NotNil(t, head.Spec.Warm)
	assert.Equal(t, "registry.example.com/neutree/neutree-serve:v1.2.0", head.Spec.Warm.Images[0].Ref)
	assertNodeComponentTypes(t, head.Spec.Components, []v1.NodeComponentType{
		v1.NodeComponentTypeRayHead,
		v1.NodeComponentTypeNodeExporter,
		v1.NodeComponentTypeAcceleratorExporter,
		v1.NodeComponentTypeMetricsAgent,
	})
	nodeExporter := findComponent(head.Spec.Components, nodeExporterComponentName)
	require.NotNil(t, nodeExporter)
	assert.Equal(t, defaultNodeExporterImage, nodeExporter.Image)
	exporter := findComponent(head.Spec.Components, acceleratorExporterComponentName)
	require.NotNil(t, exporter)
	assert.Equal(t, "nvcr.io/nvidia/k8s/dcgm-exporter:test", exporter.Image)
	assert.Equal(t, []string{"--gpus all", "--cap-add=SYS_ADMIN"}, exporter.DockerRunOptions)
	assert.Equal(t, 9400, exporter.Ports[0].Port)

	vmagentComponent := findComponent(head.Spec.Components, vmagentComponentName)
	require.NotNil(t, vmagentComponent)
	assert.Equal(t, defaultVMAgentImage, vmagentComponent.Image)
	assert.NotEmpty(t, vmagentComponent.ConfigHash)
	vmagentConfig := findConfigFile(vmagentComponent.ConfigFiles, vmagentConfigPath)
	require.NotNil(t, vmagentConfig)
	assert.Contains(t, vmagentConfig.Content, `job_name: static-node-node-exporter`)
	assert.Contains(t, vmagentConfig.Content, `"10.0.0.10:9100"`)
	assert.Contains(t, vmagentConfig.Content, `"10.0.0.11:9100"`)
	assert.Contains(t, vmagentConfig.Content, `job_name: static-node-accelerator-exporter`)
	assert.Contains(t, vmagentConfig.Content, `"10.0.0.10:9400"`)
	assert.NotContains(t, vmagentConfig.Content, `"10.0.0.11:9400"`)
	assert.Contains(t, vmagentConfig.Content, `remote_write:`)
	assert.Contains(t, vmagentConfig.Content, `"http://vm:8480/insert/0/prometheus/"`)

	worker := nodes[1]
	require.NotNil(t, worker.Metadata)
	require.NotNil(t, worker.Spec)
	assert.Equal(t, "worker-0", worker.Metadata.Name)
	assert.Equal(t, v1.StaticNodeRoleWorker, worker.Spec.Role)
	rayWorker := findComponent(worker.Spec.Components, "ray-worker")
	require.NotNil(t, rayWorker)
	assert.Equal(t, "registry.example.com/neutree/neutree-serve:v1.2.0", rayWorker.Image)
	require.Len(t, rayWorker.Args, 1)
	assert.Contains(t, rayWorker.Args[0], "python /home/ray/start.py --address=10.0.0.10:6379")
	assert.Contains(t, rayWorker.Args[0], v1.StaticNodeProvisionType)
	assertNodeComponentTypes(t, worker.Spec.Components, []v1.NodeComponentType{
		v1.NodeComponentTypeRayWorker,
		v1.NodeComponentTypeNodeExporter,
	})

	cluster.Spec.Version = "mutated"
	assert.Equal(t, "registry.example.com/neutree/neutree-serve:v1.2.0", head.Spec.Warm.Images[0].Ref)
}

func TestStaticNodeClusterReconcilerBuildDesiredNodesValidation(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*v1.StaticNodeCluster)
		wantErr string
	}{
		{
			name: "missing head node",
			mutate: func(cluster *v1.StaticNodeCluster) {
				cluster.Spec.Head.NodeName = "missing"
			},
			wantErr: "head node missing not found",
		},
		{
			name: "missing version",
			mutate: func(cluster *v1.StaticNodeCluster) {
				cluster.Spec.Version = ""
			},
			wantErr: "static node cluster spec.version is required",
		},
		{
			name: "missing image registry",
			mutate: func(cluster *v1.StaticNodeCluster) {
				cluster.Spec.ImageRegistry = ""
			},
			wantErr: "static node cluster spec.image_registry is required",
		},
		{
			name: "duplicate node",
			mutate: func(cluster *v1.StaticNodeCluster) {
				cluster.Spec.Nodes[0].Name = "head-0"
			},
			wantErr: "duplicate static node head-0",
		},
		{
			name: "missing ip",
			mutate: func(cluster *v1.StaticNodeCluster) {
				cluster.Spec.Nodes[0].IP = ""
			},
			wantErr: "static node worker-0 ip is required",
		},
		{
			name: "missing nodes",
			mutate: func(cluster *v1.StaticNodeCluster) {
				cluster.Spec.Nodes = nil
			},
			wantErr: "static node cluster spec.nodes is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := testStaticNodeCluster()
			tt.mutate(cluster)

			_, err := (&StaticNodeClusterReconciler{}).BuildDesiredNodes(cluster, nil)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestStaticNodeClusterReconcilerAggregateStatus(t *testing.T) {
	tests := []struct {
		name       string
		nodes      []*v1.StaticNode
		wantStatus v1.StaticNodeClusterStatus
	}{
		{
			name: "ready when all nodes, warm, and metrics are ready",
			nodes: []*v1.StaticNode{
				staticNodeStatus("head-0", v1.StaticNodeRoleHead, v1.StaticNodePhaseReady, true, []v1.NodeComponentStatus{
					readyComponent(nodeExporterComponentName),
					readyComponent(vmagentComponentName),
				}),
				staticNodeStatus("worker-0", v1.StaticNodeRoleWorker, v1.StaticNodePhaseReady, true, []v1.NodeComponentStatus{
					readyComponent(nodeExporterComponentName),
				}),
			},
			wantStatus: v1.StaticNodeClusterStatus{
				Phase:        v1.StaticNodeClusterPhaseReady,
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
				staticNodeStatus("head-0", v1.StaticNodeRoleHead, v1.StaticNodePhaseReady, true, []v1.NodeComponentStatus{
					readyComponent(nodeExporterComponentName),
					readyComponent(vmagentComponentName),
				}),
				staticNodeStatus("worker-0", v1.StaticNodeRoleWorker, v1.StaticNodePhaseReconciling, false, nil),
			},
			wantStatus: v1.StaticNodeClusterStatus{
				Phase:        v1.StaticNodeClusterPhaseDegraded,
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
			wantStatus: v1.StaticNodeClusterStatus{
				Phase:        v1.StaticNodeClusterPhaseFailed,
				DesiredNodes: 2,
				ReadyNodes:   0,
				HeadReady:    false,
				MetricsReady: false,
				WarmReady:    false,
			},
		},
		{
			name: "ignores stale nodes and marks missing desired nodes not ready",
			nodes: []*v1.StaticNode{
				staticNodeStatus("worker-0", v1.StaticNodeRoleWorker, v1.StaticNodePhaseReady, true, []v1.NodeComponentStatus{
					readyComponent(nodeExporterComponentName),
				}),
				staticNodeStatus("stale-0", v1.StaticNodeRoleWorker, v1.StaticNodePhaseReady, true, []v1.NodeComponentStatus{
					readyComponent(nodeExporterComponentName),
				}),
			},
			wantStatus: v1.StaticNodeClusterStatus{
				Phase:        v1.StaticNodeClusterPhaseProvisioning,
				DesiredNodes: 2,
				ReadyNodes:   1,
				HeadReady:    false,
				MetricsReady: false,
				WarmReady:    false,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := (&StaticNodeClusterReconciler{}).AggregateStatus(testStaticNodeCluster(), tt.nodes)

			assert.Equal(t, tt.wantStatus, status)
		})
	}
}

func testStaticNodeCluster() *v1.StaticNodeCluster {
	return &v1.StaticNodeCluster{
		Metadata: &v1.Metadata{
			Workspace:   "default",
			Name:        "static-a",
			Annotations: map[string]string{"source": "unit-test"},
		},
		Spec: &v1.StaticNodeClusterSpec{
			Version:       "v1.2.0",
			ImageRegistry: "registry.example.com/neutree",
			Head: v1.StaticNodeClusterHeadSpec{
				NodeName: "head-0",
			},
			Nodes: []v1.StaticNodeClusterNodeSpec{
				{
					Name:            "worker-0",
					IP:              "10.0.0.11",
					Role:            v1.StaticNodeRoleWorker,
					SSHAuthRef:      "ssh-ref",
					SSHAuth:         &v1.Auth{SSHUser: "ray", SSHPrivateKey: "/tmp/key"},
					AcceleratorType: "",
				},
				{
					Name:            "head-0",
					IP:              "10.0.0.10",
					Role:            v1.StaticNodeRoleWorker,
					SSHAuthRef:      "ssh-ref",
					SSHAuth:         &v1.Auth{SSHUser: "ray", SSHPrivateKey: "/tmp/key"},
					AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.String(),
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
	components []v1.NodeComponentStatus,
) *v1.StaticNode {
	return &v1.StaticNode{
		Metadata: &v1.Metadata{Name: name},
		Spec:     &v1.StaticNodeSpec{Role: role},
		Status: &v1.StaticNodeStatus{
			Phase:      phase,
			Warm:       &v1.WarmStatus{Ready: warmReady},
			Components: components,
		},
	}
}

func readyComponent(name string) v1.NodeComponentStatus {
	return v1.NodeComponentStatus{
		Name:  name,
		Ready: true,
		Phase: v1.NodeComponentPhaseRunning,
	}
}

func assertNodeComponentTypes(t *testing.T, components []v1.NodeComponentSpec, want []v1.NodeComponentType) {
	t.Helper()

	require.Len(t, components, len(want))
	for i, component := range components {
		assert.Equal(t, want[i], component.Type)
	}
}

func findComponent(components []v1.NodeComponentSpec, name string) *v1.NodeComponentSpec {
	for i := range components {
		if components[i].Name == name {
			return &components[i]
		}
	}

	return nil
}

func findConfigFile(configFiles []v1.NodeComponentConfigFile, path string) *v1.NodeComponentConfigFile {
	for i := range configFiles {
		if configFiles[i].Path == path {
			return &configFiles[i]
		}
	}

	return nil
}
