package allocation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

func TestKubernetesAllocationProviderBuildsRawAcceleratorEvidence(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	endpointPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   "default",
			Name:        "chat-pod",
			UID:         "pod-uid",
			Labels:      map[string]string{"app": endpointWorkloadType, "endpoint": "chat"},
			Annotations: map[string]string{"vendor.example/devices": "raw"},
		},
		Spec: corev1.PodSpec{NodeName: "node-a"},
	}
	provider := KubernetesAllocationProvider{
		Client: fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(endpointPod).
			WithIndex(&corev1.Pod{}, "spec.nodeName", podNodeNameIndex).
			Build(),
		NodeName: "node-a",
		PodResources: PodResourceListerFunc(func(_ context.Context) ([]adapter.PodResource, error) {
			return []adapter.PodResource{{
				Namespace: "default",
				Name:      "chat-pod",
				Containers: []adapter.ContainerDevices{{
					ResourceName: "vendor.example/accelerator",
					DeviceIDs:    []string{"device-0"},
				}},
			}}, nil
		}),
	}

	evidence, err := provider.KubernetesAcceleratorEvidence(context.Background())

	require.NoError(t, err)
	assert.True(t, evidence.AllocationAvailable)
	require.Len(t, evidence.PodResources, 1)
	assert.Equal(t, "vendor.example/accelerator", evidence.PodResources[0].Containers[0].ResourceName)
	require.Len(t, evidence.EndpointPods, 1)
	assert.Equal(t, "pod-uid", evidence.EndpointPods[0].UID)
	assert.Equal(t, "raw", evidence.EndpointPods[0].Annotations["vendor.example/devices"])
}

func TestKubernetesAllocationProviderPropagatesPodResourceErrors(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	provider := KubernetesAllocationProvider{
		Client:   fake.NewClientBuilder().WithScheme(scheme).Build(),
		NodeName: "node-a",
		PodResources: PodResourceListerFunc(func(context.Context) ([]adapter.PodResource, error) {
			return nil, errors.New("pod resources unavailable")
		}),
	}

	evidence, err := provider.KubernetesAcceleratorEvidence(context.Background())

	require.Error(t, err)
	assert.Equal(t, adapter.KubernetesEvidence{}, evidence)
}

func TestKubernetesAllocationProviderWithoutDependenciesReturnsEmptyEvidence(t *testing.T) {
	evidence, err := (KubernetesAllocationProvider{}).KubernetesAcceleratorEvidence(context.Background())

	require.NoError(t, err)
	assert.Equal(t, adapter.KubernetesEvidence{}, evidence)
}

func TestRayServeAllocationProviderBuildsStaticAcceleratorEvidence(t *testing.T) {
	provider := RayServeAllocationProvider{
		Dashboard: &fakeRayDashboardService{
			nodes: []v1.NodeSummary{{IP: "10.0.0.10", Raylet: v1.Raylet{NodeID: "node-a", State: v1.AliveNodeState}}},
			applications: &dashboard.RayServeApplicationsResponse{Applications: map[string]dashboard.RayServeApplicationStatus{
				"default_chat": {
					DeployedAppConfig: &dashboard.RayServeApplication{Args: map[string]interface{}{
						"deployment_options": map[string]interface{}{
							"Backend": map[string]interface{}{"num_gpus": 0.5},
						},
					}},
					Deployments: map[string]dashboard.Deployment{
						"Backend": {Replicas: []dashboard.Replica{{NodeID: "node-a", ActorID: "actor-a", ReplicaID: "replica-a"}}},
					},
				},
			}},
			actors: map[string]dashboard.Actor{
				"actor-a": {ActorID: "actor-a", NodeID: "node-a", PID: 1234, RequiredResources: map[string]float64{"NPU": 1}},
			},
		},
		NodeIP: "10.0.0.10",
		ProcEnv: ProcessEnvReaderFunc(func(pid int) (map[string]string, error) {
			require.Equal(t, 1234, pid)
			return map[string]string{"ASCEND_VISIBLE_DEVICES": "0"}, nil
		}),
		ProcessDescendants: ProcessDescendantReaderFunc(func(pid int) ([]int, error) {
			require.Equal(t, 1234, pid)
			return []int{2345, 1234}, nil
		}),
	}

	evidence, err := provider.StaticAcceleratorEvidence(context.Background())

	require.NoError(t, err)
	assert.True(t, evidence.AllocationAvailable)
	require.Len(t, evidence.RayEvidence.Actors, 1)
	assert.Equal(t, 1.0, evidence.RayEvidence.Actors[0].RequiredResources["NPU"])
	require.Len(t, evidence.RayEvidence.Replicas, 1)
	assert.Equal(t, 0.5, evidence.RayEvidence.Replicas[0].GPUQuantity)
	assert.Equal(t, []int{1234, 2345}, evidence.RayEvidence.ActorProcesses[1234].DescendantPIDs)
	assert.Equal(t, "0", evidence.RayEvidence.ActorProcesses[1234].Environment["ASCEND_VISIBLE_DEVICES"])
}

func TestRayServeAllocationProviderKeepsActorEvidenceWhenApplicationsFail(t *testing.T) {
	provider := RayServeAllocationProvider{
		Dashboard: &fakeRayDashboardService{
			nodes:           []v1.NodeSummary{{IP: "10.0.0.10", Raylet: v1.Raylet{NodeID: "node-a", State: v1.AliveNodeState}}},
			applicationsErr: errors.New("serve applications unavailable"),
			actors: map[string]dashboard.Actor{
				"actor-a": {ActorID: "actor-a", NodeID: "node-a", PID: 1234},
			},
		},
		NodeIP: "10.0.0.10",
		ProcEnv: ProcessEnvReaderFunc(func(int) (map[string]string, error) {
			return nil, errors.New("process disappeared")
		}),
		ProcessDescendants: ProcessDescendantReaderFunc(func(pid int) ([]int, error) {
			return []int{pid}, nil
		}),
	}

	evidence, err := provider.StaticAcceleratorEvidence(context.Background())

	require.NoError(t, err)
	assert.False(t, evidence.AllocationAvailable)
	require.Len(t, evidence.RayEvidence.Actors, 1)
	assert.Empty(t, evidence.RayEvidence.Replicas)
	assert.Equal(t, []int{1234}, evidence.RayEvidence.ActorProcesses[1234].DescendantPIDs)
	assert.Empty(t, evidence.RayEvidence.ActorProcesses[1234].Environment)
}

func TestRayServeAllocationProviderWithoutNodeIPReturnsEmptyEvidence(t *testing.T) {
	evidence, err := (RayServeAllocationProvider{Dashboard: &fakeRayDashboardService{}}).
		StaticAcceleratorEvidence(context.Background())

	require.NoError(t, err)
	assert.Equal(t, adapter.StaticEvidence{}, evidence)
}

func TestProcFSEnvReaderReadsProcessEnvironment(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "1234")
	require.NoError(t, os.MkdirAll(directory, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "environ"), []byte("FOO=bar\x00EMPTY=\x00ignored"), 0o600))

	environment, err := (ProcFSEnvReader{Root: root}).Env(1234)

	require.NoError(t, err)
	assert.Equal(t, "bar", environment["FOO"])
	assert.Equal(t, "", environment["EMPTY"])
}

func TestProcFSProcessTreeReaderFindsDescendantPIDs(t *testing.T) {
	root := t.TempDir()
	writeProcStatus := func(pid, parentPID int) {
		directory := filepath.Join(root, strconv.Itoa(pid))
		require.NoError(t, os.MkdirAll(directory, 0o755))
		contents := "Name:\ttest\nPPid:\t" + strconv.Itoa(parentPID) + "\n"
		require.NoError(t, os.WriteFile(filepath.Join(directory, "status"), []byte(contents), 0o600))
	}

	writeProcStatus(100, 1)
	writeProcStatus(200, 100)
	writeProcStatus(300, 200)
	writeProcStatus(400, 1)

	pids, err := (ProcFSProcessTreeReader{Root: root}).DescendantPIDs(100)

	require.NoError(t, err)
	assert.Equal(t, []int{100, 200, 300}, pids)

}

type fakeRayDashboardService struct {
	nodes           []v1.NodeSummary
	applications    *dashboard.RayServeApplicationsResponse
	applicationsErr error
	actors          map[string]dashboard.Actor
}

func (f *fakeRayDashboardService) GetClusterMetadata() (*dashboard.ClusterMetadataResponse, error) {
	return &dashboard.ClusterMetadataResponse{}, nil
}

func (f *fakeRayDashboardService) ListNodes() ([]v1.NodeSummary, error) {
	return f.nodes, nil
}

func (f *fakeRayDashboardService) GetClusterStatus() (v1.RayAPIClusterStatus, error) {
	return v1.RayAPIClusterStatus{}, nil
}

func (f *fakeRayDashboardService) GetServeApplications() (*dashboard.RayServeApplicationsResponse, error) {
	return f.applications, f.applicationsErr
}

func (f *fakeRayDashboardService) UpdateServeApplications(dashboard.RayServeApplicationsRequest) error {
	return nil
}

func (f *fakeRayDashboardService) GetActorLog(string, string, int) (string, error) {
	return "", nil
}

func (f *fakeRayDashboardService) ListActors(
	filters []dashboard.ActorFilter,
	detail bool,
	_ int,
) (*dashboard.ActorsResponse, error) {
	nodeID := ""
	for _, filter := range filters {
		if filter.Key == "node_id" && filter.Predicate == "=" {
			nodeID = filter.Value
		}
	}

	actors := make([]dashboard.Actor, 0, len(f.actors))
	for _, actor := range f.actors {
		if nodeID == "" || actor.NodeID == nodeID {
			actors = append(actors, actorWithDetail(actor, detail))
		}
	}

	return &dashboard.ActorsResponse{
		Result: true,
		Data: dashboard.ActorsResponseData{
			Result: dashboard.ActorsListResult{Result: actors},
		},
	}, nil
}

// actorWithDetail mirrors the Ray State API contract: required_resources is
// present only on a detailed actor response. This makes the static-evidence
// test fail if the production request stops asking for detail=true.
func actorWithDetail(actor dashboard.Actor, detail bool) dashboard.Actor {
	result := actor
	if !detail || len(actor.RequiredResources) == 0 {
		result.RequiredResources = nil

		return result
	}

	result.RequiredResources = make(map[string]float64, len(actor.RequiredResources))
	for resource, quantity := range actor.RequiredResources {
		result.RequiredResources[resource] = quantity
	}

	return result
}

func podNodeNameIndex(object client.Object) []string {
	pod, ok := object.(*corev1.Pod)
	if !ok || pod.Spec.NodeName == "" {
		return nil
	}

	return []string{pod.Spec.NodeName}
}
