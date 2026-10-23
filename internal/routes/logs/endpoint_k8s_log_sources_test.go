package logs

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
)

func TestGetK8sLogSources_Metadata(test *testing.T) {
	tests := []struct {
		name       string
		pods       []corev1.Pod
		statusCode int
		errorText  string
	}{
		{name: "container metadata", pods: []corev1.Pod{*endpointLogTestPod()}, statusCode: http.StatusOK},
		{name: "no pods", statusCode: http.StatusOK, errorText: "no pods found"},
		{name: "API failure", statusCode: http.StatusForbidden, errorText: "failed to get endpoint logs info"},
	}
	for _, testCase := range tests {
		test.Run(testCase.name, func(test *testing.T) {
			cluster := logSourcesTestCluster(test, testCase.pods, testCase.statusCode)
			endpoint := &v1.Endpoint{Metadata: &v1.Metadata{Workspace: "default", Name: "test-endpoint"}}
			response, err := getK8sLogSources(cluster, endpoint, "default", "test-endpoint")
			if testCase.errorText != "" {
				require.ErrorContains(test, err, testCase.errorText)
				assert.Nil(test, response)

				return
			}

			require.NoError(test, err)
			encoded, err := json.Marshal(response)
			require.NoError(test, err)
			assert.JSONEq(test, `{"deployments":[{"name":"Backend","replicas":[{
				"replica_id":"pod-123",
				"logs":[{"type":"logs","url":"/api/v1/endpoints/default/test-endpoint/logs/pod-123/logs",
				"download_url":"/api/v1/endpoints/default/test-endpoint/logs/pod-123/logs?download=true"}],
				"containers":[{"name":"serve","type":"container","restart_count":1,"started":true},
				{"name":"sidecar","type":"container","restart_count":0,"started":false},
				{"name":"model-downloader","type":"initContainer","restart_count":1,"started":true}]
			}]}]}`, string(encoded))
		})
	}
}

func TestGetK8sLogSources_Started(test *testing.T) {
	startupProbeIncomplete := false
	cases := []struct {
		name         string
		statuses     []corev1.ContainerStatus
		started      bool
		restartCount int32
	}{
		{
			name: "inference running before startup probe completes",
			statuses: []corev1.ContainerStatus{{
				Name: "inference", Started: &startupProbeIncomplete,
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
			}},
			started: true,
		},
		{
			name: "inference terminated",
			statuses: []corev1.ContainerStatus{{
				Name: "inference", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1}},
			}},
			started: true,
		},
		{
			name: "inference previously crashed",
			statuses: []corev1.ContainerStatus{{
				Name: "inference", Started: &startupProbeIncomplete,
				State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1}},
			}},
			started: true,
		},
		{
			name: "inference restarted without termination state",
			statuses: []corev1.ContainerStatus{{
				Name: "inference", RestartCount: 2,
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
			}},
			started: true, restartCount: 2,
		},
		{
			name: "inference waiting before first start",
			statuses: []corev1.ContainerStatus{{
				Name: "inference", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}},
			}},
		},
		{name: "inference status missing"},
	}
	for _, testCase := range cases {
		test.Run(testCase.name, func(test *testing.T) {
			pod := corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "pod-123"},
				Spec: corev1.PodSpec{
					Containers:     []corev1.Container{{Name: "inference"}},
					InitContainers: []corev1.Container{{Name: "model-downloader"}},
				},
				Status: corev1.PodStatus{
					ContainerStatuses: testCase.statuses,
					InitContainerStatuses: []corev1.ContainerStatus{{
						Name: "model-downloader", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{}},
					}},
				},
			}
			cluster := logSourcesTestCluster(test, []corev1.Pod{pod}, http.StatusOK)
			endpoint := &v1.Endpoint{Metadata: &v1.Metadata{Workspace: "default", Name: "test-endpoint"}}
			response, err := getK8sLogSources(cluster, endpoint, "default", "test-endpoint")
			require.NoError(test, err)
			require.Len(test, response.Deployments, 1)
			require.Len(test, response.Deployments[0].Replicas, 1)
			assert.Equal(test, []util.ContainerLogInfo{
				{Name: "inference", Type: "container", RestartCount: testCase.restartCount, Started: testCase.started},
				{Name: "model-downloader", Type: "initContainer", Started: true},
			}, response.Deployments[0].Replicas[0].Containers)
		})
	}
}

func TestReplicaInfo_OmitsKubernetesMetadataForRay(test *testing.T) {
	encoded, err := json.Marshal(ReplicaInfo{ReplicaID: "failed-replica", Failed: true, Logs: []LogInfo{
		{Type: "stderr", URL: "/stderr", DownloadURL: "/stderr?download=true"},
	}})
	require.NoError(test, err)
	assert.NotContains(test, string(encoded), "containers")
	assert.Contains(test, string(encoded), `"failed":true`)
}

func logSourcesTestCluster(test *testing.T, pods []corev1.Pod, statusCode int) *v1.Cluster {
	test.Helper()
	cluster := &v1.Cluster{Metadata: &v1.Metadata{Workspace: "default", Name: "test-cluster"}}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		var response interface{}
		switch request.URL.Path {
		case "/api":
			response = metav1.APIVersions{Versions: []string{"v1"}}
		case "/apis":
			response = metav1.APIGroupList{}
		case "/api/v1":
			response = metav1.APIResourceList{GroupVersion: "v1", APIResources: []metav1.APIResource{
				{Name: "pods", Kind: "Pod", Namespaced: true, Verbs: metav1.Verbs{"list"}},
			}}
		case "/api/v1/namespaces/" + util.ClusterNamespace(cluster) + "/pods":
			assert.Equal(test, "app=inference,endpoint=test-endpoint,workspace=default", request.URL.Query().Get("labelSelector"))
			if statusCode != http.StatusOK {
				http.Error(writer, "list denied", statusCode)

				return
			}
			response = corev1.PodList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"}, Items: pods}
		default:
			test.Errorf("unexpected Kubernetes API request: %s", request.URL)
			http.NotFound(writer, request)

			return
		}
		assert.NoError(test, json.NewEncoder(writer).Encode(response))
	}))
	test.Cleanup(server.Close)
	kubeconfig := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: test
  cluster:
    server: %s
contexts:
- name: test
  context:
    cluster: test
current-context: test
`, server.URL)
	cluster.Spec = &v1.ClusterSpec{Type: v1.KubernetesClusterType, Config: &v1.ClusterConfig{
		KubernetesConfig: &v1.KubernetesClusterConfig{Kubeconfig: base64.StdEncoding.EncodeToString([]byte(kubeconfig))},
	}}

	return cluster
}
