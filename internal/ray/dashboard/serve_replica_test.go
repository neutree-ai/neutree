package dashboard_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/neutree-ai/neutree/internal/ray/dashboard"
	dashboardmocks "github.com/neutree-ai/neutree/internal/ray/dashboard/mocks"
)

func TestExtractReplicaShortID(t *testing.T) {
	tests := []struct {
		name     string
		actor    string
		expected string
	}{
		{"well-formed serve replica name", "SERVE_REPLICA::default_test#deploy-1#abc123", "abc123"},
		{"name with no hash", "weird-name", ""},
		{"name ending with hash returns empty", "SERVE_REPLICA::a#b#", ""},
		{"empty name", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, dashboard.ExtractReplicaShortID(tt.actor))
		})
	}
}

func TestReplicaShortIDFromActor(t *testing.T) {
	tests := []struct {
		name     string
		actor    dashboard.Actor
		expected string
	}{
		{
			name:     "well-formed name returns short_id",
			actor:    dashboard.Actor{ActorID: "actor-aa", Name: "SERVE_REPLICA::default_test#deploy-1#shortxyz"},
			expected: "shortxyz",
		},
		{
			name:     "non-conventional name falls back to actor_id",
			actor:    dashboard.Actor{ActorID: "actor-bb", Name: "weird-name"},
			expected: "actor-bb",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, dashboard.ReplicaShortIDFromActor(&tt.actor))
		})
	}
}

func TestListFailedActorsForDeployment_BuildsExpectedFilters(t *testing.T) {
	svc := dashboardmocks.NewMockDashboardService(t)

	svc.EXPECT().
		ListActors(mock.MatchedBy(func(filters []dashboard.ActorFilter) bool {
			hasClass, hasState := false, false
			for _, f := range filters {
				if f.Key == "class_name" && f.Predicate == "=" && f.Value == "ServeReplica:default_app:deploy-1" {
					hasClass = true
				}
				if f.Key == "state" && f.Predicate == "=" && f.Value == "DEAD" {
					hasState = true
				}
			}
			return hasClass && hasState
		}), true, 100).
		Return(&dashboard.ActorsResponse{
			Data: dashboard.ActorsResponseData{Result: dashboard.ActorsListResult{Result: []dashboard.Actor{{ActorID: "x"}}}},
		}, nil).
		Once()

	actors, err := dashboard.ListFailedActorsForDeployment(svc, "default_app", "deploy-1")

	require.NoError(t, err)
	require.Len(t, actors, 1)
	assert.Equal(t, "x", actors[0].ActorID)
}

func TestListFailedActorsForDeployment_PropagatesError(t *testing.T) {
	svc := dashboardmocks.NewMockDashboardService(t)
	svc.EXPECT().ListActors(mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("boom")).Once()

	_, err := dashboard.ListFailedActorsForDeployment(svc, "app", "dep")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "ServeReplica:app:dep")
	assert.Contains(t, err.Error(), "boom")
}

func TestFindFailedActorForDeployment_PicksMostRecentByStartTime(t *testing.T) {
	svc := dashboardmocks.NewMockDashboardService(t)

	// Highest actor_id intentionally on oldest start_time so a regression
	// to actor_id ordering would flip the assertion.
	svc.EXPECT().ListActors(mock.Anything, true, mock.Anything).Return(&dashboard.ActorsResponse{
		Data: dashboard.ActorsResponseData{Result: dashboard.ActorsListResult{Result: []dashboard.Actor{
			{ActorID: "actor-zzz", Name: "SERVE_REPLICA::a#d#oldshort", StartTime: 1000},
			{ActorID: "actor-mmm", Name: "SERVE_REPLICA::a#d#midshort", StartTime: 2000},
			{ActorID: "actor-aaa", Name: "SERVE_REPLICA::a#d#newshort", StartTime: 3000},
		}}},
	}, nil).Once()

	got, err := dashboard.FindFailedActorForDeployment(svc, "a", "d")

	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "actor-aaa", got.ActorID)
}

func TestFindFailedActorForDeployment_StartTimeTieBrokenByActorID(t *testing.T) {
	svc := dashboardmocks.NewMockDashboardService(t)

	svc.EXPECT().ListActors(mock.Anything, true, mock.Anything).Return(&dashboard.ActorsResponse{
		Data: dashboard.ActorsResponseData{Result: dashboard.ActorsListResult{Result: []dashboard.Actor{
			{ActorID: "actor-aaa", Name: "SERVE_REPLICA::a#d#aa", StartTime: 5000},
			{ActorID: "actor-zzz", Name: "SERVE_REPLICA::a#d#zz", StartTime: 5000},
		}}},
	}, nil).Once()

	got, err := dashboard.FindFailedActorForDeployment(svc, "a", "d")

	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "actor-zzz", got.ActorID)
}

func TestFindFailedActorForDeployment_NoActorsReturnsNil(t *testing.T) {
	svc := dashboardmocks.NewMockDashboardService(t)
	svc.EXPECT().ListActors(mock.Anything, mock.Anything, mock.Anything).Return(&dashboard.ActorsResponse{}, nil).Once()

	got, err := dashboard.FindFailedActorForDeployment(svc, "a", "d")

	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestFindFailedActorByReplicaID_MatchesShortID(t *testing.T) {
	svc := dashboardmocks.NewMockDashboardService(t)
	svc.EXPECT().ListActors(mock.Anything, mock.Anything, mock.Anything).Return(&dashboard.ActorsResponse{
		Data: dashboard.ActorsResponseData{Result: dashboard.ActorsListResult{Result: []dashboard.Actor{
			{ActorID: "actor-1", Name: "SERVE_REPLICA::a#d#wanted"},
			{ActorID: "actor-2", Name: "SERVE_REPLICA::a#d#other"},
		}}},
	}, nil).Once()

	got, err := dashboard.FindFailedActorByReplicaID(svc, "a", "d", "wanted")

	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "actor-1", got.ActorID)
}

func TestFindFailedActorByReplicaID_FallsBackToActorID(t *testing.T) {
	svc := dashboardmocks.NewMockDashboardService(t)
	svc.EXPECT().ListActors(mock.Anything, mock.Anything, mock.Anything).Return(&dashboard.ActorsResponse{
		Data: dashboard.ActorsResponseData{Result: dashboard.ActorsListResult{Result: []dashboard.Actor{
			{ActorID: "weird-id", Name: "non-conventional-name"},
		}}},
	}, nil).Once()

	got, err := dashboard.FindFailedActorByReplicaID(svc, "a", "d", "weird-id")

	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "weird-id", got.ActorID)
}

func TestFindFailedActorByReplicaID_NoMatchReturnsNil(t *testing.T) {
	svc := dashboardmocks.NewMockDashboardService(t)
	svc.EXPECT().ListActors(mock.Anything, mock.Anything, mock.Anything).Return(&dashboard.ActorsResponse{
		Data: dashboard.ActorsResponseData{Result: dashboard.ActorsListResult{Result: []dashboard.Actor{
			{ActorID: "x", Name: "SERVE_REPLICA::a#d#other"},
		}}},
	}, nil).Once()

	got, err := dashboard.FindFailedActorByReplicaID(svc, "a", "d", "missing")

	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestLookupFailedActorAcrossDeployments_DeterministicOrder(t *testing.T) {
	svc := dashboardmocks.NewMockDashboardService(t)

	// Both deployments have a matching actor. Sorted iteration must visit
	// "alpha" before "beta", so the alpha actor wins.
	svc.EXPECT().
		ListActors(mock.MatchedBy(func(filters []dashboard.ActorFilter) bool {
			for _, f := range filters {
				if f.Key == "class_name" && f.Value == "ServeReplica:app:alpha" {
					return true
				}
			}
			return false
		}), mock.Anything, mock.Anything).
		Return(&dashboard.ActorsResponse{
			Data: dashboard.ActorsResponseData{Result: dashboard.ActorsListResult{Result: []dashboard.Actor{
				{ActorID: "alpha-actor", Name: "SERVE_REPLICA::app#alpha#wanted"},
			}}},
		}, nil).
		Once()

	got, err := dashboard.LookupFailedActorAcrossDeployments(svc, "app", []string{"beta", "alpha"}, "wanted")

	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "alpha-actor", got.ActorID)
}

func TestLookupFailedActorAcrossDeployments_NoMatchReturnsNil(t *testing.T) {
	svc := dashboardmocks.NewMockDashboardService(t)
	svc.EXPECT().ListActors(mock.Anything, mock.Anything, mock.Anything).Return(&dashboard.ActorsResponse{
		Data: dashboard.ActorsResponseData{Result: dashboard.ActorsListResult{Result: nil}},
	}, nil).Twice()

	got, err := dashboard.LookupFailedActorAcrossDeployments(svc, "app", []string{"d1", "d2"}, "missing")

	require.NoError(t, err)
	assert.Nil(t, got)
}
