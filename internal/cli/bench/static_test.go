package bench

import (
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
)

func TestSelectBackendReplica(t *testing.T) {
	for _, tc := range []struct {
		name        string
		deployments map[string]dashboard.Deployment
		wantActor   string
		wantNode    string
	}{
		{
			name: "sorts replicas by stable identity",
			deployments: map[string]dashboard.Deployment{
				"Backend": {
					Replicas: []dashboard.Replica{
						{NodeID: "node-b", ReplicaID: "replica-b", ActorID: "actor-b"},
						{NodeID: "node-a", ReplicaID: "replica-a", ActorID: "actor-a"},
					},
				},
				"Controller": {Replicas: []dashboard.Replica{{ActorID: "controller"}}},
			},
			wantActor: "actor-a",
			wantNode:  "node-a",
		},
		{
			name: "prefers a running replica",
			deployments: map[string]dashboard.Deployment{
				"Backend": {
					Replicas: []dashboard.Replica{
						{NodeID: "node-a", ReplicaID: "replica-a", ActorID: "actor-a", State: "STARTING"},
						{NodeID: "node-b", ReplicaID: "replica-b", ActorID: "actor-b", State: "RUNNING"},
					},
				},
			},
			wantActor: "actor-b",
			wantNode:  "node-b",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			replica, err := selectBackendReplica(tc.deployments)
			require.NoError(t, err)
			require.Equal(t, tc.wantActor, replica.ActorID)
			require.Equal(t, tc.wantNode, replica.NodeID)
		})
	}
}

func TestDockerContainerIDFromCgroup(t *testing.T) {
	tests := []struct {
		name    string
		cgroup  string
		want    string
		wantErr string
	}{
		{
			name:   "docker scope",
			cgroup: "0::/system.slice/docker-0123456789abcdef0123456789abcdef.scope\n",
			want:   "0123456789abcdef0123456789abcdef",
		},
		{
			name:   "docker directory",
			cgroup: "12:devices:/docker/abcdef0123456789abcdef0123456789\n",
			want:   "abcdef0123456789abcdef0123456789",
		},
		{
			name:    "missing docker id",
			cgroup:  "0::/user.slice/user-1000.slice\n",
			wantErr: "no Docker container ID",
		},
		{
			name: "ambiguous docker ids",
			cgroup: "0::/docker/0123456789abcdef0123456789abcdef\n" +
				"1:cpu:/docker/abcdef0123456789abcdef0123456789\n",
			wantErr: "ambiguous Docker container IDs",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := dockerContainerIDFromCgroup(tt.cgroup)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestStaticServeBaseURLUsesServeRoute(t *testing.T) {
	baseURL, err := staticServeBaseURL(&v1.Cluster{
		Status: &v1.ClusterStatus{DashboardURL: "http://10.0.0.10:8265"},
	}, &v1.Endpoint{Metadata: &v1.Metadata{Workspace: "default", Name: "chat"}})
	require.NoError(t, err)
	require.Equal(t, "http://10.0.0.10:8000/default/chat", baseURL)
}

func TestBackendNodeIDFallsBackToReplica(t *testing.T) {
	for _, tc := range []struct {
		name    string
		actor   *dashboard.Actor
		replica dashboard.Replica
		want    string
	}{
		{
			name:    "uses the actor node",
			actor:   &dashboard.Actor{NodeID: "actor-node"},
			replica: dashboard.Replica{NodeID: "replica-node"},
			want:    "actor-node",
		},
		{
			name:    "falls back to the replica node",
			actor:   &dashboard.Actor{},
			replica: dashboard.Replica{NodeID: "replica-node"},
			want:    "replica-node",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, backendNodeID(tc.actor, tc.replica))
		})
	}
}
