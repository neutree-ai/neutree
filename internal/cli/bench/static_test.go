package bench

import (
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
)

func TestSelectBackendReplicaUsesStableIdentity(t *testing.T) {
	deployments := map[string]dashboard.Deployment{
		"Backend": {
			Replicas: []dashboard.Replica{
				{NodeID: "node-b", ReplicaID: "replica-b", ActorID: "actor-b"},
				{NodeID: "node-a", ReplicaID: "replica-a", ActorID: "actor-a"},
			},
		},
		"Controller": {Replicas: []dashboard.Replica{{ActorID: "controller"}}},
	}

	replica, err := selectBackendReplica(deployments)
	require.NoError(t, err)
	require.Equal(t, "actor-a", replica.ActorID)
	require.Equal(t, "node-a", replica.NodeID)
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

func TestStaticTargetHelpersUseDeployedModelPathAndServeRoute(t *testing.T) {
	tokenizer, err := tokenizerPathFromRayApplication(dashboard.RayServeApplicationStatus{
		DeployedAppConfig: &dashboard.RayServeApplication{Args: map[string]interface{}{
			"model": map[string]interface{}{"path": "/models/deployed"},
		}},
	})
	require.NoError(t, err)
	require.Equal(t, "/models/deployed", tokenizer)

	baseURL, err := staticServeBaseURL(&v1.Cluster{
		Status: &v1.ClusterStatus{DashboardURL: "http://10.0.0.10:8265"},
	}, &v1.Endpoint{Metadata: &v1.Metadata{Workspace: "default", Name: "chat"}})
	require.NoError(t, err)
	require.Equal(t, "http://10.0.0.10:8000/default/chat", baseURL)
}

func TestBackendNodeIDFallsBackToReplica(t *testing.T) {
	replica := dashboard.Replica{NodeID: "replica-node"}
	require.Equal(t, "actor-node", backendNodeID(&dashboard.Actor{NodeID: "actor-node"}, replica))
	require.Equal(t, "replica-node", backendNodeID(&dashboard.Actor{}, replica))
}
