package rayserve

import (
	"sort"
	"strings"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
)

func NodeIDByIP(service dashboard.DashboardService, nodeIP string) (string, error) {
	if service == nil || nodeIP == "" {
		return "", nil
	}

	nodes, err := service.ListNodes()
	if err != nil {
		return "", err
	}

	for _, node := range nodes {
		if node.IP == nodeIP && node.Raylet.State == v1.AliveNodeState {
			return node.Raylet.NodeID, nil
		}
	}

	return "", nil
}

func ActorByID(service dashboard.DashboardService, actorID string) (*dashboard.Actor, error) {
	actors, err := service.ListActors(
		[]dashboard.ActorFilter{{Key: "actor_id", Predicate: "=", Value: actorID}},
		true,
		1,
	)
	if err != nil {
		return nil, err
	}

	if actors == nil || len(actors.Data.Result.Result) == 0 {
		return nil, nil
	}

	return &actors.Data.Result.Result[0], nil
}

func SortedServeApplicationNames(resp *dashboard.RayServeApplicationsResponse) []string {
	if resp == nil {
		return nil
	}

	names := make([]string, 0, len(resp.Applications))
	for name := range resp.Applications {
		names = append(names, name)
	}

	sort.Strings(names)

	return names
}

func SortedDeploymentNames(deployments map[string]dashboard.Deployment) []string {
	names := make([]string, 0, len(deployments))
	for name := range deployments {
		names = append(names, name)
	}

	sort.Strings(names)

	return names
}

func ApplicationIdentity(
	appName string,
	status dashboard.RayServeApplicationStatus,
) (string, string) {
	if status.DeployedAppConfig != nil {
		workspace, endpoint, ok := parseRoutePrefix(status.DeployedAppConfig.RoutePrefix)
		if ok {
			return workspace, endpoint
		}
	}

	workspace, endpoint, ok := strings.Cut(appName, "_")
	if !ok {
		return "", appName
	}

	return workspace, endpoint
}

func parseRoutePrefix(routePrefix string) (string, string, bool) {
	routePrefix = strings.Trim(routePrefix, "/")
	if routePrefix == "" {
		return "", "", false
	}

	workspace, endpoint, ok := strings.Cut(routePrefix, "/")
	if !ok || workspace == "" || endpoint == "" || strings.Contains(endpoint, "/") {
		return "", "", false
	}

	return workspace, endpoint, true
}
