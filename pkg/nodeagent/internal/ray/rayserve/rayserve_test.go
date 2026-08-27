package rayserve

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/nodeagent/internal/ray/dashboard"
)

func TestNodeIDByIPSelectsAliveNode(t *testing.T) {
	service := &testDashboardService{nodes: []v1.NodeSummary{
		{IP: "10.0.0.1", Raylet: v1.Raylet{NodeID: "dead-node", State: v1.DeadNodeState}},
		{IP: "10.0.0.1", Raylet: v1.Raylet{NodeID: "alive-node", State: v1.AliveNodeState}},
	}}

	nodeID, err := NodeIDByIP(service, "10.0.0.1")

	require.NoError(t, err)
	assert.Equal(t, "alive-node", nodeID)
}

func TestActorByIDUsesActorFilter(t *testing.T) {
	service := &testDashboardService{actors: []dashboard.Actor{{ActorID: "actor-a", PID: 1234}}}

	actor, err := ActorByID(service, "actor-a")

	require.NoError(t, err)
	require.NotNil(t, actor)
	assert.Equal(t, "actor-a", actor.ActorID)
	assert.Equal(t, 1234, actor.PID)
	assert.Equal(t, []dashboard.ActorFilter{{Key: "actor_id", Predicate: "=", Value: "actor-a"}}, service.actorFilters)
}

func TestApplicationIdentityAndSortedNames(t *testing.T) {
	tests := []struct {
		name      string
		appName   string
		status    dashboard.RayServeApplicationStatus
		workspace string
		endpoint  string
	}{
		{
			name:    "uses route prefix",
			appName: "fallback_name",
			status: dashboard.RayServeApplicationStatus{DeployedAppConfig: &dashboard.RayServeApplication{
				RoutePrefix: "/default/chat/",
			}},
			workspace: "default",
			endpoint:  "chat",
		},
		{
			name:    "falls back when route prefix has too many segments",
			appName: "default_chat",
			status: dashboard.RayServeApplicationStatus{DeployedAppConfig: &dashboard.RayServeApplication{
				RoutePrefix: "/default/chat/extra",
			}},
			workspace: "default",
			endpoint:  "chat",
		},
		{name: "preserves unscoped app name", appName: "chat", endpoint: "chat"},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			workspace, endpoint := ApplicationIdentity(testCase.appName, testCase.status)

			assert.Equal(t, testCase.workspace, workspace)
			assert.Equal(t, testCase.endpoint, endpoint)
		})
	}

	assert.Equal(t, []string{"alpha", "zeta"}, SortedServeApplicationNames(&dashboard.RayServeApplicationsResponse{
		Applications: map[string]dashboard.RayServeApplicationStatus{"zeta": {}, "alpha": {}},
	}))
	assert.Equal(t, []string{"alpha", "zeta"}, SortedDeploymentNames(map[string]dashboard.Deployment{
		"zeta":  {},
		"alpha": {},
	}))
}

type testDashboardService struct {
	nodes        []v1.NodeSummary
	actors       []dashboard.Actor
	actorFilters []dashboard.ActorFilter
}

func (s *testDashboardService) ListNodes() ([]v1.NodeSummary, error) {
	return s.nodes, nil
}

func (s *testDashboardService) GetServeApplications() (*dashboard.RayServeApplicationsResponse, error) {
	return nil, nil
}

func (s *testDashboardService) ListActors(
	filters []dashboard.ActorFilter,
	_ bool,
	_ int,
) (*dashboard.ActorsResponse, error) {
	s.actorFilters = append([]dashboard.ActorFilter(nil), filters...)

	return &dashboard.ActorsResponse{
		Data: dashboard.ActorsResponseData{Result: dashboard.ActorsListResult{Result: s.actors}},
	}, nil
}

var _ dashboard.DashboardService = (*testDashboardService)(nil)
