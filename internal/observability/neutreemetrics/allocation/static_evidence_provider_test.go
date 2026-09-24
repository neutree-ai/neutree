package allocation

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
)

func TestRayServeAllocationProviderBuildsStaticAcceleratorEvidence(t *testing.T) {
	root := t.TempDir()
	writeProcStatusFile(t, root, 1234, 100)
	writeProcStatusFile(t, root, 2345, 1234)
	writeProcEnvironFile(t, root, 1234, "VENDOR_VISIBLE_DEVICES=0")

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
		NodeIP:     "10.0.0.10",
		ProcFSRoot: root,
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
	assert.Equal(t, 100, evidence.RayEvidence.ActorProcesses[1234].ParentPID)
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
		// Readable but empty: the actor's PID is not in it, so every per-process
		// read misses the way it does once the process is gone.
		ProcFSRoot: t.TempDir(),
	}

	evidence, err := provider.StaticAcceleratorEvidence(context.Background())

	require.NoError(t, err)
	assert.False(t, evidence.AllocationAvailable)
	require.Len(t, evidence.RayEvidence.Actors, 1)
	assert.Empty(t, evidence.RayEvidence.Replicas)
	assert.Equal(t, []int{1234}, evidence.RayEvidence.ActorProcesses[1234].DescendantPIDs)
	assert.Empty(t, evidence.RayEvidence.ActorProcesses[1234].Environment)
}

func TestRayServeAllocationProviderExcludesDeadActorsAndTheirReplicas(t *testing.T) {
	root := t.TempDir()
	writeProcStatusFile(t, root, 1234, 1)

	provider := RayServeAllocationProvider{
		Dashboard: &fakeRayDashboardService{
			nodes: []v1.NodeSummary{{IP: "10.0.0.10", Raylet: v1.Raylet{NodeID: "node-a", State: v1.AliveNodeState}}},
			applications: &dashboard.RayServeApplicationsResponse{Applications: map[string]dashboard.RayServeApplicationStatus{
				"default_chat": {
					Deployments: map[string]dashboard.Deployment{
						"Backend": {Replicas: []dashboard.Replica{
							{NodeID: "node-a", ActorID: "actor-live", ReplicaID: "replica-live"},
							{NodeID: "node-a", ActorID: "actor-dead", ReplicaID: "replica-dead"},
							{NodeID: "node-a", ActorID: "actor-gone", ReplicaID: "replica-gone"},
						}},
					},
				},
			}},
			actors: map[string]dashboard.Actor{
				"actor-live": {ActorID: "actor-live", NodeID: "node-a", PID: 1234, State: "ALIVE"},
				"actor-dead": {ActorID: "actor-dead", NodeID: "node-a", PID: 5678, State: "DEAD"},
			},
		},
		NodeIP: "10.0.0.10",
		// Only the live actor's PID exists: the dead one's process is gone.
		ProcFSRoot: root,
	}

	evidence, err := provider.StaticAcceleratorEvidence(context.Background())

	require.NoError(t, err)
	require.Len(t, evidence.RayEvidence.Actors, 1)
	assert.Equal(t, "actor-live", evidence.RayEvidence.Actors[0].ActorID)

	// A DEAD actor runs no process, so it cannot be holding accelerator memory.
	// A replica whose actor is absent is equally unallocated — the process that
	// would own the device is gone. Both must drop out, and dropping them here
	// keeps the two sides of this evidence set in agreement instead of leaving
	// the adapter to read the mismatch as incomplete evidence and suppress the
	// node's whole allocation view.
	replicaIDs := make([]string, 0, len(evidence.RayEvidence.Replicas))
	for _, replica := range evidence.RayEvidence.Replicas {
		replicaIDs = append(replicaIDs, replica.ReplicaID)
	}

	assert.Equal(t, []string{"replica-live"}, replicaIDs)

	_, probed := evidence.RayEvidence.ActorProcesses[5678]
	assert.False(t, probed, "a dead actor must not be probed for its process tree")
	_, probed = evidence.RayEvidence.ActorProcesses[1234]
	assert.True(t, probed)
}

// A snapshot is a read of every process on the node, so a collection with no
// actor to probe must not build one. The root below cannot be read, which makes
// the snapshot attempt report itself - an absent warning is the evidence that
// nothing was built.
func TestRayServeAllocationProviderBuildsNoTreeWhenNothingIsProbed(t *testing.T) {
	var logs bytes.Buffer

	// SetOutput alone is a no-op while klog still logs to stderr, which is its
	// default: output() then never reads file[]. Both are needed, and both are
	// restored.
	klog.LogToStderr(false)
	klog.SetOutput(&logs)

	t.Cleanup(func() {
		klog.LogToStderr(true)
		klog.SetOutput(os.Stderr)
	})

	provider := RayServeAllocationProvider{
		Dashboard: &fakeRayDashboardService{
			nodes:  []v1.NodeSummary{{IP: "10.0.0.10", Raylet: v1.Raylet{NodeID: "node-a", State: v1.AliveNodeState}}},
			actors: map[string]dashboard.Actor{},
		},
		NodeIP:     "10.0.0.10",
		ProcFSRoot: filepath.Join(t.TempDir(), "gone"),
	}

	evidence, err := provider.StaticAcceleratorEvidence(context.Background())

	require.NoError(t, err)

	klog.Flush()
	assert.Empty(t, evidence.RayEvidence.ActorProcesses)
	assert.NotContains(t, logs.String(), "Falling back to per-call process tree reads")

	// The control: the same unreadable root with an actor to probe does build,
	// and says so. Without it the assertion above could pass on a capture that
	// simply never sees anything.
	provider.Dashboard = &fakeRayDashboardService{
		nodes:  []v1.NodeSummary{{IP: "10.0.0.10", Raylet: v1.Raylet{NodeID: "node-a", State: v1.AliveNodeState}}},
		actors: map[string]dashboard.Actor{"actor-a": {ActorID: "actor-a", NodeID: "node-a", PID: 1234}},
	}

	_, err = provider.StaticAcceleratorEvidence(context.Background())

	require.NoError(t, err)

	klog.Flush()
	assert.Contains(t, logs.String(), "Falling back to per-call process tree reads")
}

func TestRayServeAllocationProviderWithoutNodeIPReturnsEmptyEvidence(t *testing.T) {
	evidence, err := (RayServeAllocationProvider{Dashboard: &fakeRayDashboardService{}}).StaticAcceleratorEvidence(context.Background())

	require.NoError(t, err)
	assert.Empty(t, evidence)
}

func TestRayEvidenceHelpersHandleProcRoots(t *testing.T) {
	provider := RayServeAllocationProvider{ProcFSRoot: "/custom/proc"}

	assert.Equal(t, "/custom/proc", provider.procFSRoot())
	envReader, ok := provider.processEnvReader().(ProcFSEnvReader)

	require.True(t, ok)
	assert.Equal(t, "/custom/proc", envReader.Root)
	assert.Equal(t, defaultProcFSRoot, (RayServeAllocationProvider{}).procFSRoot())
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
