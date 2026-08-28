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

	envReader := p.processEnvReader()
	descendantReader := p.processDescendantReader()
	actorProcesses := make(map[int]adapter.ProcessInfo, len(actors))

	for _, actor := range actors {
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
		replicas = rayReplicasFromApplications(applications, nodeID)
	}

	return adapter.StaticEvidence{
		// A missing Serve application response cannot distinguish an empty
		// endpoint set from unavailable allocation evidence.
		AllocationAvailable: applicationsErr == nil && applications != nil,
		RayEvidence: adapter.RayEvidence{
			Actors:         rayActorsFromDashboard(actors),
			Replicas:       replicas,
			ActorProcesses: actorProcesses,
		},
	}, nil
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

func (p RayServeAllocationProvider) processDescendantReader() ProcessDescendantReader {
	if p.ProcessDescendants != nil {
		return p.ProcessDescendants
	}

	return ProcFSProcessTreeReader{Root: p.procFSRoot()}
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
