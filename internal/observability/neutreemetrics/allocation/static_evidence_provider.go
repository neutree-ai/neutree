package allocation

import (
	"context"
	"sort"
	"strings"

	"github.com/neutree-ai/neutree/internal/ray/dashboard"
	"github.com/neutree-ai/neutree/internal/ray/rayserve"
	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

// RayServeAllocationProvider collects raw Ray and local-process topology for a
// static-cluster adapter. It does not infer accelerator ownership; the selected
// adapter joins this evidence with vendor exporter data using its own rules.
type RayServeAllocationProvider struct {
	Dashboard          dashboard.DashboardService
	DashboardURL       string
	NodeIP             string
	ProcEnv            ProcessEnvReader
	ProcessDescendants ProcessDescendantReader
}

func (p RayServeAllocationProvider) StaticAcceleratorEvidence(
	ctx context.Context,
) (adapter.StaticEvidence, error) {
	service := p.dashboardService()
	if service == nil || p.NodeIP == "" {
		return adapter.StaticEvidence{}, nil
	}

	nodeID, err := p.rayNodeID(service)
	if err != nil || nodeID == "" {
		return adapter.StaticEvidence{}, err
	}

	applications, applicationsErr := service.GetServeApplications()
	actorsResp, err := service.ListActors(
		[]dashboard.ActorFilter{{Key: "node_id", Predicate: "=", Value: nodeID}},
		true,
		0,
	)

	if err != nil {
		return adapter.StaticEvidence{}, err
	}

	actors := []dashboard.Actor{}
	if actorsResp != nil {
		actors = append(actors, actorsResp.Data.Result.Result...)
	}

	// Ray's State API keeps reporting an actor as DEAD until it is reaped, so a
	// busy node accumulates them. A DEAD actor has no process left to read, and
	// while the state API still lists it its lingering replica keeps it a valid
	// candidate whose device attribution then fails - which the adapter reads as
	// incomplete evidence.
	liveActors := make([]dashboard.Actor, 0, len(actors))
	liveActorIDs := make(map[string]struct{}, len(actors))

	for _, actor := range actors {
		if actorStateIsDead(actor.State) {
			continue
		}

		liveActors = append(liveActors, actor)

		if actorID := strings.TrimSpace(actor.ActorID); actorID != "" {
			liveActorIDs[actorID] = struct{}{}
		}
	}

	envReader := p.processEnvReader()
	descendantReader := p.processDescendantReader()
	actorProcesses := make(map[int]adapter.ProcessInfo, len(liveActors))

	for _, actor := range liveActors {
		if actor.PID <= 0 {
			continue
		}

		info, ok := p.actorProcessInfo(actor.PID, envReader, descendantReader)
		if ok {
			actorProcesses[actor.PID] = info
		}
	}

	var replicas []adapter.RayReplica
	if applicationsErr == nil {
		// Join replicas to the actors that survived, so both halves of this
		// evidence set describe the same things. The adapter reads a replica it
		// cannot match as incomplete evidence, and answers incomplete evidence by
		// suppressing the node's entire allocation view rather than reporting a
		// partial one - so one stale replica blanks the node-level metrics for
		// every endpoint on the node.
		replicas = replicasWithLiveActors(rayReplicasFromApplications(applications, nodeID), liveActorIDs)
	}

	return adapter.StaticEvidence{
		// A missing Serve application response cannot distinguish an empty
		// endpoint set from unavailable allocation evidence.
		AllocationAvailable: applicationsErr == nil && applications != nil,
		RayEvidence: adapter.RayEvidence{
			Actors:         rayActorsFromDashboard(liveActors),
			Replicas:       replicas,
			ActorProcesses: actorProcesses,
		},
	}, nil
}

// actorStateIsDead reports whether the Ray State API marked an actor as
// terminated. DEAD is its terminal state; every other state still describes an
// actor that can own a device, so an unrecognised or absent state is kept.
func actorStateIsDead(state string) bool {
	return strings.EqualFold(strings.TrimSpace(state), "DEAD")
}

// replicasWithLiveActors drops replicas whose actor did not survive.
func replicasWithLiveActors(
	replicas []adapter.RayReplica,
	liveActorIDs map[string]struct{},
) []adapter.RayReplica {
	result := make([]adapter.RayReplica, 0, len(replicas))

	for _, replica := range replicas {
		if _, ok := liveActorIDs[strings.TrimSpace(replica.ActorID)]; !ok {
			continue
		}

		result = append(result, replica)
	}

	return result
}

func rayActorsFromDashboard(actors []dashboard.Actor) []adapter.RayActor {
	result := make([]adapter.RayActor, 0, len(actors))

	for _, actor := range actors {
		resources := make(map[string]float64, len(actor.RequiredResources))
		for name, quantity := range actor.RequiredResources {
			resources[name] = quantity
		}

		result = append(result, adapter.RayActor{
			ActorID:           actor.ActorID,
			ClassName:         actor.ClassName,
			State:             actor.State,
			Name:              actor.Name,
			NodeID:            actor.NodeID,
			PID:               actor.PID,
			RequiredResources: resources,
			StartTime:         actor.StartTime,
			EndTime:           actor.EndTime,
		})
	}

	return result
}

func rayReplicasFromApplications(
	applications *dashboard.RayServeApplicationsResponse,
	nodeID string,
) []adapter.RayReplica {
	if applications == nil {
		return nil
	}

	result := make([]adapter.RayReplica, 0)

	for _, applicationName := range rayserve.SortedServeApplicationNames(applications) {
		status := applications.Applications[applicationName]
		workspace, endpoint := rayserve.ApplicationIdentity(applicationName, status)

		for _, deploymentName := range rayserve.SortedDeploymentNames(status.Deployments) {
			deployment := status.Deployments[deploymentName]
			deploymentOptions := rayDeploymentOptions(status, deploymentName)

			for _, replica := range deployment.Replicas {
				if replica.NodeID != nodeID || replica.ActorID == "" {
					continue
				}

				result = append(result, adapter.RayReplica{
					Workspace:         workspace,
					Endpoint:          endpoint,
					Deployment:        deploymentName,
					ActorID:           replica.ActorID,
					ReplicaID:         replica.ReplicaID,
					NodeID:            replica.NodeID,
					DeploymentOptions: cloneDeploymentOptions(deploymentOptions),
				})
			}
		}
	}

	return result
}

func rayDeploymentOptions(
	status dashboard.RayServeApplicationStatus,
	deploymentName string,
) map[string]interface{} {
	if status.DeployedAppConfig == nil || status.DeployedAppConfig.Args == nil || deploymentName == "" {
		return nil
	}

	rawOptions, ok := status.DeployedAppConfig.Args["deployment_options"].(map[string]interface{})
	if !ok {
		return nil
	}

	for name, raw := range rawOptions {
		if !strings.EqualFold(name, deploymentName) {
			continue
		}

		options, ok := raw.(map[string]interface{})
		if !ok {
			return nil
		}

		return cloneDeploymentOptions(options)
	}

	return nil
}

func cloneDeploymentOptions(input map[string]interface{}) map[string]interface{} {
	if len(input) == 0 {
		return nil
	}

	result := make(map[string]interface{}, len(input))
	for key, value := range input {
		result[key] = cloneDeploymentOptionValue(value)
	}

	return result
}

func cloneDeploymentOptionValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		return cloneDeploymentOptions(typed)
	case []interface{}:
		result := make([]interface{}, len(typed))
		for index, item := range typed {
			result[index] = cloneDeploymentOptionValue(item)
		}

		return result
	default:
		return value
	}
}

func (p RayServeAllocationProvider) dashboardService() dashboard.DashboardService {
	if p.Dashboard != nil {
		return p.Dashboard
	}

	if strings.TrimSpace(p.DashboardURL) == "" {
		return nil
	}

	return dashboard.NewDashboardService(p.DashboardURL)
}

func (p RayServeAllocationProvider) rayNodeID(service dashboard.DashboardService) (string, error) {
	return rayserve.NodeIDByIP(service, p.NodeIP)
}

func (p RayServeAllocationProvider) processEnvReader() ProcessEnvReader {
	if p.ProcEnv != nil {
		return p.ProcEnv
	}

	return ProcFSEnvReader{}
}

// processDescendantReader resolves the topology source for one collection.
//
// It is called once per collection, and building the snapshot here is what makes
// every actor share a single enumeration of /proc instead of paying for its own.
// An injected reader is returned untouched; a snapshot that cannot be built
// degrades to the per-call reader, which fails the way a missing /proc always
// did and which the actor loop already tolerates.
func (p RayServeAllocationProvider) processDescendantReader() ProcessDescendantReader {
	if p.ProcessDescendants != nil {
		return p.ProcessDescendants
	}

	root := p.procFSRoot()

	snapshot, err := NewCachedProcessDescendantReader(root)
	if err != nil {
		return ProcFSProcessTreeReader{Root: root}
	}

	return snapshot
}

func (p RayServeAllocationProvider) actorProcessInfo(
	pid int,
	envReader ProcessEnvReader,
	descendantReader ProcessDescendantReader,
) (adapter.ProcessInfo, bool) {
	if pid <= 0 {
		return adapter.ProcessInfo{}, false
	}

	info := adapter.ProcessInfo{
		PID:            pid,
		DescendantPIDs: actorDescendantPIDs(descendantReader, pid),
	}
	if env, err := envReader.Env(pid); err == nil {
		info.Environment = env
	}

	if parentPID, ok, err := processParentPID(p.procFSRoot(), pid); err == nil && ok {
		info.ParentPID = parentPID
	}

	return info, true
}

func (p RayServeAllocationProvider) procFSRoot() string {
	if reader, ok := p.ProcEnv.(ProcFSEnvReader); ok && strings.TrimSpace(reader.Root) != "" {
		return reader.Root
	}

	if reader, ok := p.ProcessDescendants.(ProcFSProcessTreeReader); ok && strings.TrimSpace(reader.Root) != "" {
		return reader.Root
	}

	return defaultProcFSRoot
}

func actorDescendantPIDs(reader ProcessDescendantReader, pid int) []int {
	pids := []int{pid}
	if reader == nil {
		return pids
	}

	descendants, err := reader.DescendantPIDs(pid)
	if err != nil {
		return pids
	}

	seen := map[int]struct{}{pid: {}}

	for _, descendant := range descendants {
		if descendant > 0 {
			seen[descendant] = struct{}{}
		}
	}

	pids = pids[:0]

	for descendant := range seen {
		pids = append(pids, descendant)
	}

	sort.Ints(pids)

	return pids
}
