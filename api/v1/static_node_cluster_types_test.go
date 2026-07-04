package v1

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/neutree-ai/neutree/pkg/scheme"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStaticNodeClusterKey(t *testing.T) {
	tests := []struct {
		name    string
		cluster *StaticNodeCluster
		want    string
	}{
		{
			name:    "nil metadata",
			cluster: &StaticNodeCluster{ID: 1},
			want:    "default-staticnodecluster-1",
		},
		{
			name: "default workspace",
			cluster: &StaticNodeCluster{
				ID:       2,
				Metadata: &Metadata{Name: "cluster-a"},
			},
			want: "default-staticnodecluster-2-cluster-a",
		},
		{
			name: "explicit workspace",
			cluster: &StaticNodeCluster{
				ID:       3,
				Metadata: &Metadata{Name: "cluster-a", Workspace: "workspace-a"},
			},
			want: "workspace-a-staticnodecluster-3-cluster-a",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.cluster.Key())
		})
	}
}

func TestStaticNodeKey(t *testing.T) {
	node := &StaticNode{
		ID:       1,
		Metadata: &Metadata{Name: "node-a", Workspace: "workspace-a"},
	}

	assert.Equal(t, "workspace-a-staticnode-1-node-a", node.Key())
}

func TestStaticNodeComponentJSONRoundTrip(t *testing.T) {
	node := &StaticNode{
		Kind: StaticNodeKind,
		Metadata: &Metadata{
			Name:      "head-0",
			Workspace: "default",
		},
		Spec: &StaticNodeSpec{
			Cluster: "cluster-a",
			IP:      "10.0.0.10",
			Role:    StaticNodeRoleHead,
			Warm: &WarmSpec{
				Images: []WarmImageSpec{
					{Name: "ray-runtime", Ref: "registry.example.com/neutree/serve:v1.2.0", Required: true},
				},
			},
			Components: []NodeComponentSpec{
				{
					Name:             "ray-head",
					Image:            "registry.example.com/neutree/serve:v1.2.0",
					Command:          []string{"python", "/home/ray/start.py"},
					Args:             []string{"--head"},
					Env:              map[string]string{"RAY_TMPDIR": "/tmp/ray"},
					DockerRunOptions: []string{"--net=host"},
					ConfigFiles: []NodeComponentConfigFile{
						{Path: "/etc/neutree/ray.yaml", Content: "ray: {}", Mode: "0644", Atomic: true},
					},
					HealthCheck: &NodeComponentHealthCheck{HTTPPath: "/api/version", Port: 8265},
					ConfigHash:  "hash-a",
				},
			},
		},
		Status: &StaticNodeStatus{
			Phase: StaticNodePhaseReady,
			Accelerator: &StaticNodeAcceleratorStatus{
				Type: AcceleratorTypeNVIDIAGPU.String(),
				Devices: []StaticNodeAcceleratorDeviceStatus{
					{
						ID:           "0",
						UUID:         "GPU-abc",
						ProductName:  "NVIDIA A100",
						ProductModel: "NVIDIA_A100",
						MinorNumber:  0,
						MemoryMiB:    81920,
						Healthy:      true,
					},
				},
			},
			Warm: &WarmStatus{
				Ready: true,
				Images: []WarmImageStatus{
					{Name: "ray-runtime", Ready: true, Phase: WarmPhaseReady, Digest: "sha256:abc"},
				},
			},
			Components: []NodeComponentStatus{
				{
					Name:          "ray-head",
					Ready:         true,
					Phase:         NodeComponentPhaseRunning,
					ObservedHash:  "hash-a",
					ObservedImage: "registry.example.com/neutree/serve:v1.2.0",
				},
			},
		},
	}

	data, err := json.Marshal(node)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"docker_run_options":["--net=host"]`)
	assert.Contains(t, string(data), `"config_files"`)

	decoded := &StaticNode{}
	require.NoError(t, json.Unmarshal(data, decoded))
	require.NotNil(t, decoded.Spec)
	require.Len(t, decoded.Spec.Components, 1)
	assert.Equal(t, "ray-head", decoded.Spec.Components[0].Name)
	require.NotNil(t, decoded.Status)
	require.NotNil(t, decoded.Status.Accelerator)
	assert.Equal(t, AcceleratorTypeNVIDIAGPU.String(), decoded.Status.Accelerator.Type)
	require.Len(t, decoded.Status.Accelerator.Devices, 1)
	assert.Equal(t, "GPU-abc", decoded.Status.Accelerator.Devices[0].UUID)
	assert.Equal(t, "NVIDIA_A100", decoded.Status.Accelerator.Devices[0].ProductModel)
	assert.Equal(t, int64(81920), decoded.Status.Accelerator.Devices[0].MemoryMiB)
	require.Len(t, decoded.Status.Components, 1)
	assert.Equal(t, NodeComponentPhaseRunning, decoded.Status.Components[0].Phase)
}

func TestStaticNodeAPIShapeOmitsInternalOrDerivedFields(t *testing.T) {
	clusterSpecType := reflect.TypeOf(ClusterSpec{})
	_, hasParentUpgradeStrategy := clusterSpecType.FieldByName("UpgradeStrategy")
	assert.False(t, hasParentUpgradeStrategy, "Cluster.spec must not expose upgrade_strategy; static flow owns Recreate internally")

	staticNodeClusterSpecType := reflect.TypeOf(StaticNodeClusterSpec{})
	_, hasHead := staticNodeClusterSpecType.FieldByName("Head")
	assert.False(t, hasHead, "StaticNodeCluster head must be derived from spec.nodes[].role=head")

	staticNodeClusterStatusType := reflect.TypeOf(StaticNodeClusterStatus{})
	_, hasNestedUpgrade := staticNodeClusterStatusType.FieldByName("Upgrade")
	assert.False(t, hasNestedUpgrade, "StaticNodeCluster.status must use phase + error_message instead of nested upgrade status")
	_, hasVersion := staticNodeClusterStatusType.FieldByName("Version")
	assert.True(t, hasVersion, "StaticNodeCluster.status.version is required")
	_, hasUpgradeStep := staticNodeClusterStatusType.FieldByName("UpgradeStep")
	assert.False(t, hasUpgradeStep, "StaticNodeCluster.status must not expose upgrade_step; Recreate progress belongs in error_message")
	_, hasMetricsReady := staticNodeClusterStatusType.FieldByName("MetricsReady")
	assert.False(t, hasMetricsReady, "StaticNodeCluster.status must not expose metrics readiness in static Ray scope")

	staticNodeSpecType := reflect.TypeOf(StaticNodeSpec{})
	_, hasSSHAuthRef := staticNodeSpecType.FieldByName("SSHAuthRef")
	assert.False(t, hasSSHAuthRef, "StaticNode.spec must not expose unresolved ssh_auth_ref")

	staticNodeStatusType := reflect.TypeOf(StaticNodeStatus{})
	_, hasAllocations := staticNodeStatusType.FieldByName("Allocations")
	assert.False(t, hasAllocations, "StaticNode.status must not expose allocation state in static Ray scope")

	acceleratorStatusType := reflect.TypeOf(StaticNodeAcceleratorStatus{})
	_, hasRuntimeProfile := acceleratorStatusType.FieldByName("RuntimeProfile")
	assert.False(t, hasRuntimeProfile, "StaticNode.status.accelerator must not expose runtime_profile")
	_, hasResourceName := acceleratorStatusType.FieldByName("ResourceName")
	assert.False(t, hasResourceName, "StaticNode.status.accelerator must not expose resource_name")

	componentSpecType := reflect.TypeOf(NodeComponentSpec{})
	_, hasComponentType := componentSpecType.FieldByName("Type")
	assert.False(t, hasComponentType, "NodeComponentSpec must not expose type; name is the component identity")
	for _, field := range []string{"Dependencies", "RestartPolicy", "DesiredPhase"} {
		_, ok := componentSpecType.FieldByName(field)
		assert.False(t, ok, "NodeComponentSpec must not expose %s", field)
	}

	componentStatusType := reflect.TypeOf(NodeComponentStatus{})
	_, hasComponentStatusType := componentStatusType.FieldByName("Type")
	assert.False(t, hasComponentStatusType, "NodeComponentStatus must not expose type; name is the component identity")
}

func TestStaticResourcesSchemeRegistration(t *testing.T) {
	s := scheme.NewScheme()
	require.NoError(t, AddToScheme(s))

	clusterObj, err := s.New("StaticNodeCluster")
	require.NoError(t, err)
	assert.IsType(t, &StaticNodeCluster{}, clusterObj)
	assert.Equal(t, "StaticNodeCluster", clusterObj.GetKind())

	nodeObj, err := s.New("StaticNode")
	require.NoError(t, err)
	assert.IsType(t, &StaticNode{}, nodeObj)
	assert.Equal(t, "StaticNode", nodeObj.GetKind())

	listObj, err := s.NewList("StaticNodeList")
	require.NoError(t, err)
	assert.IsType(t, &StaticNodeList{}, listObj)
	assert.Equal(t, "StaticNodeList", listObj.GetKind())

	clusterListObj, err := s.NewList("StaticNodeClusterList")
	require.NoError(t, err)
	assert.IsType(t, &StaticNodeClusterList{}, clusterListObj)
	assert.Equal(t, "StaticNodeClusterList", clusterListObj.GetKind())

	tableObj, err := s.New("static_node_clusters")
	require.NoError(t, err)
	assert.IsType(t, &StaticNodeCluster{}, tableObj)
	assert.Equal(t, "StaticNodeCluster", tableObj.GetKind())

	nodeTableObj, err := s.New("static_nodes")
	require.NoError(t, err)
	assert.IsType(t, &StaticNode{}, nodeTableObj)
	assert.Equal(t, "StaticNode", nodeTableObj.GetKind())
}

func TestStaticNodeClusterListSetItems(t *testing.T) {
	list := &StaticNodeClusterList{}
	list.SetItems([]scheme.Object{
		&StaticNodeCluster{ID: 1, Metadata: &Metadata{Name: "cluster-1"}},
		&StaticNodeCluster{ID: 2, Metadata: &Metadata{Name: "cluster-2"}},
	})

	require.Len(t, list.Items, 2)
	assert.Equal(t, "cluster-1", list.Items[0].GetName())
	assert.Equal(t, "cluster-2", list.Items[1].GetName())
	assert.Len(t, list.GetItems(), 2)
}

func TestStaticNodeListSetItems(t *testing.T) {
	list := &StaticNodeList{}
	list.SetItems([]scheme.Object{
		&StaticNode{ID: 1, Metadata: &Metadata{Name: "node-1"}},
		&StaticNode{ID: 2, Metadata: &Metadata{Name: "node-2"}},
	})

	require.Len(t, list.Items, 2)
	assert.Equal(t, "node-1", list.Items[0].GetName())
	assert.Equal(t, "node-2", list.Items[1].GetName())
	assert.Len(t, list.GetItems(), 2)
}
