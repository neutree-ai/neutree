package allocation

import (
	"context"
	"sort"
	"strings"
	"time"

	"k8s.io/klog/v2"

	"github.com/neutree-ai/neutree/internal/ray/dashboard"
	"github.com/neutree-ai/neutree/internal/ray/rayserve"
	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

// RayServeAllocationProvider collects raw Ray and local-process topology for a
// static-cluster adapter. It does not infer accelerator ownership; the selected
// adapter joins this evidence with vendor exporter data using its own rules.
type RayServeAllocationProvider struct {
	Dashboard    dashboard.DashboardService
	DashboardURL string
	NodeIP       string
	// ProcFSRoot is the /proc mount this provider reads. Everything it builds -
	// the environment reader, the process tree snapshot, the parent lookup - is
	// rooted here, which is why the root is an input and not something recovered
	// from a reader by type assertion.
	ProcFSRoot string
}

func (p RayServeAllocationProvider) StaticAcceleratorEvidence(
	ctx context.Context,
) (adapter.StaticEvidence, error) {
	// Test branch: one line per collection, so two modes can be compared on the
	// same node against the same actor population.
	started := time.Now()

	resetProbe()

	service := p.dashboardService()
	if service == nil {
		// dashboardService reports why it could not build one.
		return adapter.StaticEvidence{}, nil
	}

	if p.NodeIP == "" {
		klog.Warningf("Static accelerator evidence is skipped: no node IP is configured")

		return adapter.StaticEvidence{}, nil
	}

	nodeID, err := p.rayNodeID(service)
	if err != nil || nodeID == "" {
		// A failed lookup reaches the caller as an error and is logged there;
		// an empty node ID returns silently, so it is the one to report here.
		if err == nil {
			klog.Warningf(
				"Static accelerator evidence is skipped: node %q is not in the Ray dashboard's node list",
				p.NodeIP,
			)
		}

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

	// Without serve applications the replicas cannot be resolved, so this
	// collection will carry actors but no allocations - and the adapter answers
	// that by emitting nothing at all, quietly.
	if applicationsErr != nil {
		klog.Warningf(
			"Static accelerator evidence for node %q has no serve applications: %v",
			p.NodeIP, applicationsErr,
		)
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
	filterStale := !keepStale()

	liveActors := make([]dashboard.Actor, 0, len(actors))
	liveActorIDs := make(map[string]struct{}, len(actors))

	for _, actor := range actors {
		if filterStale && actorStateIsDead(actor.State) {
			continue
		}

		liveActors = append(liveActors, actor)

		if actorID := strings.TrimSpace(actor.ActorID); actorID != "" {
			liveActorIDs[actorID] = struct{}{}
		}
	}

	envReader := p.processEnvReader()
	actorProcesses := make(map[int]adapter.ProcessInfo, len(liveActors))

	// Built on first use. A snapshot is a read of every process on the node, and
	// a node with no actor to probe - idle, or every actor already dead - should
	// not pay for one it will never query.
	var descendantReader ProcessDescendantReader

	for _, actor := range liveActors {
		if actor.PID <= 0 {
			continue
		}

		if descendantReader == nil {
			descendantReader = newProcessTree(p.procFSRoot())
		}

		info, ok := p.actorProcessInfo(actor.PID, envReader, descendantReader)
		if ok {
			actorProcesses[actor.PID] = info
		}
	}

	var replicas []adapter.RayReplica

	replicaCount := 0

	if applicationsErr == nil {
		// Join replicas to the actors that survived, so both halves of this
		// evidence set describe the same things. The adapter reads a replica it
		// cannot match as incomplete evidence, and answers incomplete evidence by
		// suppressing the node's entire allocation view rather than reporting a
		// partial one - so one stale replica blanks the node-level metrics for
		// every endpoint on the node.
		all := rayReplicasFromApplications(applications, nodeID)
		replicaCount = len(all)

		if filterStale {
			all = replicasWithLiveActors(all, liveActorIDs)
		}

		replicas = all
	}

	reportCollection(len(actors), len(liveActors), replicaCount, len(replicas), started)

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
		// Not reported here: the node agent never builds this provider without a
		// URL - acceleratorEvidenceProviders reports that where it decides. A
		// provider assembled by hand and left empty simply has nowhere to read.
		return nil
	}

	return dashboard.NewDashboardService(p.DashboardURL)
}

func (p RayServeAllocationProvider) rayNodeID(service dashboard.DashboardService) (string, error) {
	return rayserve.NodeIDByIP(service, p.NodeIP)
}

func (p RayServeAllocationProvider) processEnvReader() ProcessEnvReader {
	return ProcFSEnvReader{Root: p.procFSRoot()}
}

// Each read below is silent on its own: the actor keeps its place and the field
// is simply left out. The actor's process being unreadable is worth a line when
// it happens - it is what makes the adapter drop the candidate and mark the
// collection incomplete - and naming the PID is the part that helps.
//
// A node reaps processes between listing its actors and reading them, so a line
// or two here is ordinary churn. Every actor reporting it means the tree being
// read is not the one they live in.
func (p RayServeAllocationProvider) actorProcessInfo(
	pid int,
	envReader ProcessEnvReader,
	descendantReader ProcessDescendantReader,
) (adapter.ProcessInfo, bool) {
	if pid <= 0 {
		return adapter.ProcessInfo{}, false
	}

	descendants, err := actorDescendantPIDs(descendantReader, pid)
	if err != nil {
		klog.Warningf("No process tree for actor %d: %v", pid, err)
	}

	info := adapter.ProcessInfo{
		PID:            pid,
		DescendantPIDs: descendants,
	}
	if env, err := envReader.Env(pid); err == nil {
		info.Environment = env
	} else {
		klog.Warningf("No environment for actor %d: %v", pid, err)
	}

	if parentPID, ok, err := processParentPID(p.procFSRoot(), pid); err == nil && ok {
		info.ParentPID = parentPID
	} else {
		klog.Warningf("No parent for actor %d", pid)
	}

	return info, true
}

func (p RayServeAllocationProvider) procFSRoot() string {
	if strings.TrimSpace(p.ProcFSRoot) == "" {
		return defaultProcFSRoot
	}

	return p.ProcFSRoot
}

// actorDescendantPIDs degrades to the actor's own PID when the tree cannot be
// read, and returns the reason alongside it so the caller can total the misses
// instead of each one going unreported.
func actorDescendantPIDs(reader ProcessDescendantReader, pid int) ([]int, error) {
	pids := []int{pid}
	if reader == nil {
		return pids, nil
	}

	descendants, err := reader.DescendantPIDs(pid)
	if err != nil {
		return pids, err
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

	return pids, nil
}
