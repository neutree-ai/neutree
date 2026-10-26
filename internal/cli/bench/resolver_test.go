package bench

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
	internalutil "github.com/neutree-ai/neutree/internal/util"
)

type fakeDashboardService struct {
	applications    *dashboard.RayServeApplicationsResponse
	applicationsErr error
	nodes           []v1.NodeSummary
	nodesErr        error
	actors          *dashboard.ActorsResponse
	actorsErr       error
}

func (f fakeDashboardService) GetClusterMetadata() (*dashboard.ClusterMetadataResponse, error) {
	return nil, nil
}

func (f fakeDashboardService) ListNodes() ([]v1.NodeSummary, error) {
	return f.nodes, f.nodesErr
}

func (f fakeDashboardService) GetClusterStatus() (v1.RayAPIClusterStatus, error) {
	return v1.RayAPIClusterStatus{}, nil
}

func (f fakeDashboardService) GetServeApplications() (*dashboard.RayServeApplicationsResponse, error) {
	return f.applications, f.applicationsErr
}

func (f fakeDashboardService) UpdateServeApplications(dashboard.RayServeApplicationsRequest) error {
	return nil
}

func (f fakeDashboardService) GetActorLog(string, string, int) (string, error) {
	return "", nil
}

func (f fakeDashboardService) ListActors([]dashboard.ActorFilter, bool, int) (*dashboard.ActorsResponse, error) {
	return f.actors, f.actorsErr
}

func TestResolveStaticTargetCancelsBlockedDashboardRequest(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() {
		_, err := resolveStaticTarget(ctx,
			&v1.Cluster{Status: &v1.ClusterStatus{DashboardURL: server.URL}}, nil,
			&v1.Endpoint{Metadata: &v1.Metadata{Workspace: "default", Name: "chat"}})
		done <- err
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("dashboard request did not start")
	}
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("dashboard request did not stop after cancellation")
	}
}

func TestResolveStaticTargetUsesBackendActorAndDockerContainer(t *testing.T) {
	containerID := strings.Repeat("a", 64)
	sshDirectory := t.TempDir()
	sshProgram := filepath.Join(sshDirectory, "ssh")
	sshScript := fmt.Sprintf(`#!/bin/sh
last=""
for value in "$@"; do
  last="$value"
done
case "$last" in
  "cat -- /proc/42/cgroup") printf '0::/docker/%s\n' ;;
  *"docker inspect --format"*) printf '%s\n' ;;
	*"docker exec"*) printf '%%s\n' "$last" ;;
  *) echo "unexpected command: $last" >&2; exit 2 ;;
esac
`, containerID, containerID)
	require.NoError(t, os.WriteFile(sshProgram, []byte(sshScript), 0700))
	t.Setenv("PATH", sshDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))

	service := fakeDashboardService{
		applications: &dashboard.RayServeApplicationsResponse{Applications: map[string]dashboard.RayServeApplicationStatus{
			"default_chat": {
				DeployedAppConfig: &dashboard.RayServeApplication{Args: map[string]interface{}{
					"model": map[string]interface{}{"path": "/models/chat"},
				}},
				Deployments: map[string]dashboard.Deployment{
					"Backend": {Replicas: []dashboard.Replica{{ActorID: "actor-a", ReplicaID: "replica-a", NodeID: "node-a"}}},
				},
			},
		}},
		actors: &dashboard.ActorsResponse{Data: dashboard.ActorsResponseData{Result: dashboard.ActorsListResult{
			Result: []dashboard.Actor{{ActorID: "actor-a", NodeID: "node-a", PID: 42}},
		}}},
		nodes: []v1.NodeSummary{{
			IP:     "10.0.0.21",
			Raylet: v1.Raylet{NodeID: "node-a", State: v1.AliveNodeState},
		}},
	}
	previousFactory := dashboard.NewDashboardService
	dashboard.NewDashboardService = func(string) dashboard.DashboardService { return service }
	t.Cleanup(func() { dashboard.NewDashboardService = previousFactory })

	cluster := &v1.Cluster{
		Metadata: &v1.Metadata{Workspace: "default", Name: "cluster-a"},
		Status:   &v1.ClusterStatus{DashboardURL: "http://10.0.0.10:8265"},
	}
	credentialCluster := &v1.Cluster{Spec: &v1.ClusterSpec{Config: &v1.ClusterConfig{
		SSHConfig: &v1.RaySSHProvisionClusterConfig{Auth: v1.Auth{
			SSHUser:       "root",
			SSHPrivateKey: base64.StdEncoding.EncodeToString([]byte("private-key")),
		}},
	}}}
	endpoint := &v1.Endpoint{Metadata: &v1.Metadata{Workspace: "default", Name: "chat"}}

	target, err := resolveStaticTarget(context.Background(), cluster, credentialCluster, endpoint)
	require.NoError(t, err)
	t.Cleanup(func() { _ = target.Close() })
	require.Equal(t, "ssh", target.Platform)
	require.Equal(t, "docker/"+containerID+"@10.0.0.21", target.RuntimeID)
	require.Equal(t, "http://10.0.0.10:8000/default/chat", target.BaseURL)
	require.Equal(t, "/models/chat", target.Tokenizer)

	output, err := runRemoteOutput(context.Background(), target.runner, "echo ignored")
	require.NoError(t, err)
	require.Contains(t, output, "docker exec")
}

func TestResolveKubernetesTargetUsesRouterAndSelectedPod(t *testing.T) {
	cluster := &v1.Cluster{
		Metadata: &v1.Metadata{Workspace: "default", Name: "cluster-a"},
		Spec:     &v1.ClusterSpec{Type: v1.KubernetesClusterType},
	}
	endpoint := &v1.Endpoint{Metadata: &v1.Metadata{Workspace: "default", Name: "chat"}}
	namespace := internalutil.ClusterNamespace(cluster)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, http.MethodGet, request.Method)
		require.Equal(t, "/api/v1/namespaces/"+namespace+"/pods", request.URL.Path)
		require.Equal(t, "app=inference,endpoint=chat", request.URL.Query().Get("labelSelector"))
		writer.Header().Set("Content-Type", "application/json")
		pods := corev1.PodList{Items: []corev1.Pod{{
			ObjectMeta: metav1.ObjectMeta{Name: "chat-a"},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Name:    "vllm",
				Command: []string{"vllm", "serve", "/models/chat"},
			}}},
		}}}
		require.NoError(t, json.NewEncoder(writer).Encode(pods))
	}))
	t.Cleanup(server.Close)

	credentialCluster := &v1.Cluster{Spec: &v1.ClusterSpec{Config: &v1.ClusterConfig{
		KubernetesConfig: &v1.KubernetesClusterConfig{Kubeconfig: encodedKubeconfig(server.URL)},
	}}}

	target, err := resolveKubernetesTarget(context.Background(), cluster, credentialCluster, endpoint)
	require.NoError(t, err)
	require.Equal(t, "kubernetes", target.Platform)
	require.Equal(t, "pod/chat-a:vllm", target.RuntimeID)
	require.Equal(t, "/models/chat", target.Tokenizer)
	require.Equal(t, "http://router-service."+namespace+".svc.cluster.local:8000/default/chat", target.BaseURL)
}

func TestResolveTargetRejectsUnsupportedClusterType(t *testing.T) {
	_, err := resolveTarget(context.Background(), &v1.Endpoint{}, &v1.Cluster{
		Spec: &v1.ClusterSpec{Type: "unsupported"},
	}, &v1.Cluster{})

	require.ErrorContains(t, err, "unsupported Cluster type")
}

func encodedKubeconfig(serverURL string) string {
	kubeconfig := strings.Join([]string{
		"apiVersion: v1",
		"kind: Config",
		"clusters:",
		"- cluster:",
		"    server: " + serverURL,
		"  name: test",
		"contexts:",
		"- context:",
		"    cluster: test",
		"    user: test",
		"  name: test",
		"current-context: test",
		"users:",
		"- name: test",
		"  user: {}",
		"",
	}, "\n")

	return base64.StdEncoding.EncodeToString([]byte(kubeconfig))
}
