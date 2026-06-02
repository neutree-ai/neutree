package v1

import (
	"encoding/json"
	"testing"

	"github.com/neutree-ai/neutree/pkg/scheme"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStaticClusterKey(t *testing.T) {
	tests := []struct {
		name    string
		cluster *StaticCluster
		want    string
	}{
		{
			name:    "nil metadata",
			cluster: &StaticCluster{ID: 1},
			want:    "default-staticcluster-1",
		},
		{
			name: "default workspace",
			cluster: &StaticCluster{
				ID:       2,
				Metadata: &Metadata{Name: "cluster-a"},
			},
			want: "default-staticcluster-2-cluster-a",
		},
		{
			name: "explicit workspace",
			cluster: &StaticCluster{
				ID:       3,
				Metadata: &Metadata{Name: "cluster-a", Workspace: "workspace-a"},
			},
			want: "workspace-a-staticcluster-3-cluster-a",
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

func TestStaticNodeWorkerJSONRoundTrip(t *testing.T) {
	node := &StaticNode{
		Kind: "StaticNode",
		Metadata: &Metadata{
			Name:      "head-0",
			Workspace: "default",
		},
		Spec: &StaticNodeSpec{
			Cluster:         "cluster-a",
			IP:              "10.0.0.10",
			Role:            StaticNodeRoleHead,
			AcceleratorType: AcceleratorTypeNVIDIAGPU.String(),
			Warm: &WarmSpec{
				Images: []WarmImageSpec{
					{Name: "ray-runtime", Ref: "registry.example.com/neutree/serve:v1.2.0", Required: true},
				},
			},
			Workers: []NodeWorkerSpec{
				{
					Name:             "ray-head",
					Type:             NodeWorkerTypeRayHead,
					Image:            "registry.example.com/neutree/serve:v1.2.0",
					Command:          []string{"python", "/home/ray/start.py"},
					Args:             []string{"--head"},
					Env:              map[string]string{"RAY_TMPDIR": "/tmp/ray"},
					DockerRunOptions: []string{"--net=host"},
					ConfigFiles: []NodeWorkerConfigFile{
						{Path: "/etc/neutree/vmagent.yaml", Content: "scrape_configs: []", Mode: "0644", Atomic: true},
					},
					HealthCheck:   &NodeWorkerHealthCheck{HTTPPath: "/api/version", Port: 8265},
					RestartPolicy: NodeWorkerRestartPolicyAlways,
					ConfigHash:    "hash-a",
				},
			},
		},
		Status: &StaticNodeStatus{
			Phase: StaticNodePhaseReady,
			Warm: &WarmStatus{
				Ready: true,
				Images: []WarmImageStatus{
					{Name: "ray-runtime", Ready: true, Phase: WarmPhaseReady, Digest: "sha256:abc"},
				},
			},
			Workers: []NodeWorkerStatus{
				{
					Name:          "ray-head",
					Type:          NodeWorkerTypeRayHead,
					Ready:         true,
					Phase:         NodeWorkerPhaseRunning,
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
	require.Len(t, decoded.Spec.Workers, 1)
	assert.Equal(t, NodeWorkerTypeRayHead, decoded.Spec.Workers[0].Type)
	assert.Equal(t, NodeWorkerRestartPolicyAlways, decoded.Spec.Workers[0].RestartPolicy)
	require.NotNil(t, decoded.Status)
	require.Len(t, decoded.Status.Workers, 1)
	assert.Equal(t, NodeWorkerPhaseRunning, decoded.Status.Workers[0].Phase)
}

func TestStaticResourcesSchemeRegistration(t *testing.T) {
	s := scheme.NewScheme()
	require.NoError(t, AddToScheme(s))

	clusterObj, err := s.New("StaticCluster")
	require.NoError(t, err)
	assert.IsType(t, &StaticCluster{}, clusterObj)
	assert.Equal(t, "StaticCluster", clusterObj.GetKind())

	nodeObj, err := s.New("StaticNode")
	require.NoError(t, err)
	assert.IsType(t, &StaticNode{}, nodeObj)
	assert.Equal(t, "StaticNode", nodeObj.GetKind())

	listObj, err := s.NewList("StaticNodeList")
	require.NoError(t, err)
	assert.IsType(t, &StaticNodeList{}, listObj)
	assert.Equal(t, "StaticNodeList", listObj.GetKind())
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
