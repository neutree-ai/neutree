package allocation

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
)

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
				"actor-a": {ActorID: "actor-a", NodeID: "node-a", PID: 1234, RequiredResources: map[string]float64{"vendor.com/accelerator": 1}},
			},
		},
		NodeIP: "10.0.0.10",
		ProcEnv: ProcessEnvReaderFunc(func(pid int) (map[string]string, error) {
			require.Equal(t, 1234, pid)
			return map[string]string{"VENDOR_VISIBLE_DEVICES": "0"}, nil
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
	assert.Equal(t, 1.0, evidence.RayEvidence.Actors[0].RequiredResources["vendor.com/accelerator"])
	require.Len(t, evidence.RayEvidence.Replicas, 1)
	assert.Equal(t, "actor-a", evidence.RayEvidence.Replicas[0].ActorID)
	assert.Equal(t, map[string]interface{}{"num_gpus": 0.5}, evidence.RayEvidence.Replicas[0].DeploymentOptions)
	assert.Equal(t, []int{1234, 2345}, evidence.RayEvidence.ActorProcesses[1234].DescendantPIDs)
	assert.Equal(t, "0", evidence.RayEvidence.ActorProcesses[1234].Environment["VENDOR_VISIBLE_DEVICES"])
}

func TestRayServeAllocationProviderKeepsActorEvidenceWhenApplicationsFail(t *testing.T) {
	provider := RayServeAllocationProvider{
		Dashboard: &fakeRayDashboardService{
			nodes:           []v1.NodeSummary{{IP: "10.0.0.10", Raylet: v1.Raylet{NodeID: "node-a", State: v1.AliveNodeState}}},
			applicationsErr: errors.New("serve applications unavailable"),
			actors:          map[string]dashboard.Actor{"actor-a": {ActorID: "actor-a", NodeID: "node-a", PID: 1234}},
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
	evidence, err := (RayServeAllocationProvider{Dashboard: &fakeRayDashboardService{}}).StaticAcceleratorEvidence(context.Background())

	require.NoError(t, err)
	assert.Empty(t, evidence)
}

func TestRayEvidenceHelpersHandleProcRoots(t *testing.T) {
	provider := RayServeAllocationProvider{
		ProcEnv:            ProcFSEnvReader{Root: "/custom/proc"},
		ProcessDescendants: ProcFSProcessTreeReader{Root: "/custom/descendants"},
	}

	assert.Equal(t, "/custom/proc", provider.procFSRoot())
	assert.NotNil(t, provider.processEnvReader())
	assert.NotNil(t, provider.processDescendantReader())
	assert.Nil(t, (RayServeAllocationProvider{}).dashboardService())
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

	return &dashboard.ActorsResponse{Result: true, Data: dashboard.ActorsResponseData{
		Result: dashboard.ActorsListResult{Result: actors},
	}}, nil
}

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
