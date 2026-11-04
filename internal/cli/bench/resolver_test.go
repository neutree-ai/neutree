package bench

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/neutree-ai/neutree/api/v1"
	internalutil "github.com/neutree-ai/neutree/internal/util"
)

func TestResolveKubernetesTargetUsesRouterAndSelectedPod(t *testing.T) {
	cluster := &v1.Cluster{
		Metadata: &v1.Metadata{Workspace: "default", Name: "cluster-a"},
		Spec:     &v1.ClusterSpec{Type: v1.KubernetesClusterType},
	}
	endpoint := &v1.Endpoint{Metadata: &v1.Metadata{Workspace: "default", Name: "chat"}}
	namespace := internalutil.ClusterNamespace(cluster)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, http.MethodGet, request.Method)
		writer.Header().Set("Content-Type", "application/json")

		// The pod listing goes through the controller-runtime client, which
		// discovers the core group before it lists resources.
		if request.URL.Path == "/api/v1" {
			require.NoError(t, json.NewEncoder(writer).Encode(&metav1.APIResourceList{
				GroupVersion: "v1",
				APIResources: []metav1.APIResource{{
					Name:       "pods",
					Namespaced: true,
					Kind:       "Pod",
					Verbs:      []string{"get", "list"},
				}},
			}))

			return
		}

		require.Equal(t, "/api/v1/namespaces/"+namespace+"/pods", request.URL.Path)
		require.Equal(t, "app=inference,endpoint=chat,workspace=default", request.URL.Query().Get("labelSelector"))
		writer.Header().Set("Content-Type", "application/json")
		pods := corev1.PodList{Items: []corev1.Pod{{
			ObjectMeta: metav1.ObjectMeta{Name: "chat-a"},
			Status: corev1.PodStatus{
				Phase:      corev1.PodRunning,
				Conditions: readyConditions(),
			},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "vllm"}}},
		}}}
		require.NoError(t, json.NewEncoder(writer).Encode(pods))
	}))
	t.Cleanup(server.Close)

	credentialCluster := &v1.Cluster{
		Metadata: &v1.Metadata{Workspace: "default", Name: "cluster-a"},
		Spec: &v1.ClusterSpec{Config: &v1.ClusterConfig{
			KubernetesConfig: &v1.KubernetesClusterConfig{Kubeconfig: encodedKubeconfig(server.URL)},
		}}}

	target, err := resolveKubernetesTarget(context.Background(), cluster, credentialCluster, endpoint)
	require.NoError(t, err)
	require.Equal(t, "kubernetes", target.Platform)
	require.Equal(t, "pod/chat-a:vllm", target.RuntimeID)
	require.Equal(t, "http://router-service."+namespace+".svc.cluster.local:8000/default/chat", target.BaseURL)
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
