package util

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestGetPodLogContainersSpecOrder(test *testing.T) {
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers:     []corev1.Container{{Name: "engine"}, {Name: "sidecar"}},
			InitContainers: []corev1.Container{{Name: "download"}, {Name: "prepare"}},
		},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "sidecar", RestartCount: 4},
				{Name: "engine", RestartCount: 2},
			},
			InitContainerStatuses: []corev1.ContainerStatus{
				{Name: "prepare", RestartCount: 7},
				{Name: "download", RestartCount: 3},
			},
		},
	}
	originalPod := pod.DeepCopy()

	assert.Equal(test, []ContainerLogInfo{
		{Name: "engine", Type: "container", RestartCount: 2, Started: true},
		{Name: "sidecar", Type: "container", RestartCount: 4, Started: true},
		{Name: "download", Type: "initContainer", RestartCount: 3, Started: true},
		{Name: "prepare", Type: "initContainer", RestartCount: 7, Started: true},
	}, GetPodLogContainers(pod))
	assert.Equal(test, originalPod, pod)
}

func TestGetPodLogContainersStatusMatching(test *testing.T) {
	cases := []struct {
		name             string
		statuses         []corev1.ContainerStatus
		initStatuses     []corev1.ContainerStatus
		containerRestart int32
		initRestart      int32
	}{
		{name: "missing statuses"},
		{
			name: "only ordinary status", containerRestart: 5,
			statuses: []corev1.ContainerStatus{{Name: "engine", RestartCount: 5}},
		},
		{
			name: "only init status", initRestart: 8,
			initStatuses: []corev1.ContainerStatus{{Name: "download", RestartCount: 8}},
		},
		{
			name: "orphan statuses from opposite groups",
			statuses: []corev1.ContainerStatus{
				{Name: "orphan", RestartCount: 9}, {Name: "download", RestartCount: 11},
			},
			initStatuses: []corev1.ContainerStatus{{Name: "engine", RestartCount: 12}},
		},
	}
	for _, testCase := range cases {
		test.Run(testCase.name, func(test *testing.T) {
			pod := &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers:     []corev1.Container{{Name: "engine"}},
					InitContainers: []corev1.Container{{Name: "download"}},
				},
				Status: corev1.PodStatus{
					ContainerStatuses: testCase.statuses, InitContainerStatuses: testCase.initStatuses,
				},
			}
			assert.Equal(test, []ContainerLogInfo{
				{Name: "engine", Type: "container", RestartCount: testCase.containerRestart, Started: testCase.containerRestart > 0},
				{Name: "download", Type: "initContainer", RestartCount: testCase.initRestart, Started: testCase.initRestart > 0},
			}, GetPodLogContainers(pod))
		})
	}
}

func TestGetPodLogContainersIndependentRestartCounts(test *testing.T) {
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers:     []corev1.Container{{Name: "engine"}},
			InitContainers: []corev1.Container{{Name: "download"}},
		},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "download", RestartCount: 20}, {Name: "engine", RestartCount: 2},
			},
			InitContainerStatuses: []corev1.ContainerStatus{
				{Name: "engine", RestartCount: 30}, {Name: "download", RestartCount: 3},
			},
		},
	}

	assert.Equal(test, []ContainerLogInfo{
		{Name: "engine", Type: "container", RestartCount: 2, Started: true},
		{Name: "download", Type: "initContainer", RestartCount: 3, Started: true},
	}, GetPodLogContainers(pod))
}

func TestGetPodLogContainersCompletedInit(test *testing.T) {
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{InitContainers: []corev1.Container{{Name: "download"}}},
		Status: corev1.PodStatus{
			InitContainerStatuses: []corev1.ContainerStatus{{
				Name: "download", RestartCount: 4,
				State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}},
			}},
		},
	}

	assert.Equal(test, []ContainerLogInfo{
		{Name: "download", Type: "initContainer", RestartCount: 4, Started: true},
	}, GetPodLogContainers(pod))
}

func TestGetPodLogContainersStarted(test *testing.T) {
	startupProbeIncomplete := false
	startupProbeComplete := true
	cases := []struct {
		name     string
		statuses []corev1.ContainerStatus
		started  bool
	}{
		{name: "missing status"},
		{
			name:     "empty state",
			statuses: []corev1.ContainerStatus{{Name: "inference"}},
		},
		{
			name: "waiting before first start",
			statuses: []corev1.ContainerStatus{{
				Name: "inference", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}},
			}},
		},
		{
			name: "startup probe flag alone is not runtime evidence",
			statuses: []corev1.ContainerStatus{{
				Name: "inference", Started: &startupProbeComplete,
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{}},
			}},
		},
		{
			name: "running before startup probe completes",
			statuses: []corev1.ContainerStatus{{
				Name: "inference", Started: &startupProbeIncomplete,
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
			}},
			started: true,
		},
		{
			name: "running without startup probe status",
			statuses: []corev1.ContainerStatus{{
				Name: "inference", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
			}},
			started: true,
		},
		{
			name: "completed without restarting",
			statuses: []corev1.ContainerStatus{{
				Name: "inference", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}},
			}},
			started: true,
		},
		{
			name: "terminated after crash",
			statuses: []corev1.ContainerStatus{{
				Name: "inference", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1}},
			}},
			started: true,
		},
		{
			name: "waiting after previous crash without restart count",
			statuses: []corev1.ContainerStatus{{
				Name: "inference", Started: &startupProbeIncomplete,
				State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1}},
			}},
			started: true,
		},
		{
			name: "previous termination without current state",
			statuses: []corev1.ContainerStatus{{
				Name: "inference", LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{}},
			}},
			started: true,
		},
		{
			name: "waiting after restart without termination state",
			statuses: []corev1.ContainerStatus{{
				Name: "inference", RestartCount: 2,
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
			}},
			started: true,
		},
		{
			name:     "restart count without runtime state",
			statuses: []corev1.ContainerStatus{{Name: "inference", RestartCount: 1}},
			started:  true,
		},
	}
	for _, testCase := range cases {
		for _, containerType := range []string{"container", "initContainer"} {
			test.Run(testCase.name+"/"+containerType, func(test *testing.T) {
				containerStatuses := testCase.statuses
				var initStatuses []corev1.ContainerStatus
				if containerType == "initContainer" {
					containerStatuses, initStatuses = initStatuses, containerStatuses
				}
				pod := &corev1.Pod{
					Spec: corev1.PodSpec{
						Containers:     []corev1.Container{{Name: "inference"}},
						InitContainers: []corev1.Container{{Name: "inference"}},
					},
					Status: corev1.PodStatus{ContainerStatuses: containerStatuses, InitContainerStatuses: initStatuses},
				}
				originalPod := pod.DeepCopy()
				containers := GetPodLogContainers(pod)
				require.Len(test, containers, 2)
				for _, container := range containers {
					assert.Equal(test, container.Type == containerType && testCase.started, container.Started, container.Type)
				}
				assert.Equal(test, originalPod, pod)
			})
		}
	}
}

func TestGetPodLogContainersEmpty(test *testing.T) {
	cases := []struct {
		name string
		pod  corev1.Pod
	}{
		{name: "no containers"},
		{
			name: "orphan statuses only",
			pod: corev1.Pod{Status: corev1.PodStatus{
				ContainerStatuses:     []corev1.ContainerStatus{{Name: "engine", RestartCount: 2}},
				InitContainerStatuses: []corev1.ContainerStatus{{Name: "download", RestartCount: 3}},
			}},
		},
		{
			name: "ephemeral containers only",
			pod: corev1.Pod{
				Spec: corev1.PodSpec{EphemeralContainers: []corev1.EphemeralContainer{{
					EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "debug"},
				}}},
				Status: corev1.PodStatus{
					EphemeralContainerStatuses: []corev1.ContainerStatus{{Name: "debug", RestartCount: 6}},
				},
			},
		},
	}
	for _, testCase := range cases {
		test.Run(testCase.name, func(test *testing.T) {
			assert.Equal(test, []ContainerLogInfo{}, GetPodLogContainers(&testCase.pod))
		})
	}
}

func TestContainerLogInfoJSON(test *testing.T) {
	cases := []struct {
		name     string
		info     ContainerLogInfo
		expected string
	}{
		{
			name:     "ordinary container includes zero restart count",
			info:     ContainerLogInfo{Name: "engine", Type: "container"},
			expected: `{"name":"engine","type":"container","restart_count":0,"started":false}`,
		},
		{
			name:     "init container",
			info:     ContainerLogInfo{Name: "download", Type: "initContainer", RestartCount: 3, Started: true},
			expected: `{"name":"download","type":"initContainer","restart_count":3,"started":true}`,
		},
	}
	for _, testCase := range cases {
		test.Run(testCase.name, func(test *testing.T) {
			encoded, err := json.Marshal(testCase.info)
			require.NoError(test, err)
			assert.JSONEq(test, testCase.expected, string(encoded))
		})
	}
}

func TestGetEndpointLogsInfoMetadata(test *testing.T) {
	pods := []corev1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "replica-running"},
			Spec: corev1.PodSpec{
				Containers:     []corev1.Container{{Name: "engine"}, {Name: "sidecar"}},
				InitContainers: []corev1.Container{{Name: "download"}},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{
					{Name: "sidecar", RestartCount: 4}, {Name: "engine", RestartCount: 2},
				},
				InitContainerStatuses: []corev1.ContainerStatus{{Name: "download", RestartCount: 3}},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "replica-pending"},
			Spec:       corev1.PodSpec{InitContainers: []corev1.Container{{Name: "prepare"}}},
			Status:     corev1.PodStatus{Phase: corev1.PodPending},
		},
	}
	cluster := newEndpointLogsTestCluster(test, pods, http.StatusOK)
	endpoint := &v1.Endpoint{Metadata: &v1.Metadata{Name: "endpoint", Workspace: "workspace"}}

	logInfos, err := GetEndpointLogsInfo(context.Background(), cluster, endpoint)

	require.NoError(test, err)
	assert.Equal(test, []EndpointLogInfo{
		{
			ReplicaID: "replica-running", PodName: "replica-running", ContainerName: "engine", Status: "Running",
			Containers: []ContainerLogInfo{
				{Name: "engine", Type: "container", RestartCount: 2, Started: true},
				{Name: "sidecar", Type: "container", RestartCount: 4, Started: true},
				{Name: "download", Type: "initContainer", RestartCount: 3, Started: true},
			},
		},
		{
			ReplicaID: "replica-pending", PodName: "replica-pending", Status: "Pending",
			Containers: []ContainerLogInfo{{Name: "prepare", Type: "initContainer"}},
		},
	}, logInfos)
}

func TestGetEndpointLogsInfoEmptyAndListError(test *testing.T) {
	cases := []struct {
		name       string
		statusCode int
		wantError  bool
	}{
		{name: "empty pod list", statusCode: http.StatusOK},
		{name: "pod list forbidden", statusCode: http.StatusForbidden, wantError: true},
	}
	for _, testCase := range cases {
		test.Run(testCase.name, func(test *testing.T) {
			cluster := newEndpointLogsTestCluster(test, []corev1.Pod{}, testCase.statusCode)
			endpoint := &v1.Endpoint{Metadata: &v1.Metadata{Name: "endpoint", Workspace: "workspace"}}
			logInfos, err := GetEndpointLogsInfo(context.Background(), cluster, endpoint)
			assert.Nil(test, logInfos)
			if testCase.wantError {
				require.ErrorContains(test, err, "failed to list pods for endpoint endpoint")
				return
			}
			require.NoError(test, err)
		})
	}
}

func TestGetEndpointLogsInfoInvalidCluster(test *testing.T) {
	cluster := &v1.Cluster{Metadata: &v1.Metadata{Name: "cluster"}}
	endpoint := &v1.Endpoint{Metadata: &v1.Metadata{Name: "endpoint"}}

	logInfos, err := GetEndpointLogsInfo(context.Background(), cluster, endpoint)

	assert.Nil(test, logInfos)
	require.ErrorContains(test, err, "failed to get kubernetes client for cluster cluster")
}

func newEndpointLogsTestCluster(test *testing.T, pods []corev1.Pod, statusCode int) *v1.Cluster {
	test.Helper()
	cluster := &v1.Cluster{Metadata: &v1.Metadata{Name: "cluster", Workspace: "workspace"}}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		assert.Equal(test, http.MethodGet, request.Method)
		var response interface{}
		switch request.URL.Path {
		case "/api":
			response = metav1.APIVersions{Versions: []string{"v1"}}
		case "/apis":
			response = metav1.APIGroupList{}
		case "/api/v1":
			response = metav1.APIResourceList{
				GroupVersion: "v1",
				APIResources: []metav1.APIResource{{Name: "pods", Kind: "Pod", Namespaced: true, Verbs: metav1.Verbs{"list"}}},
			}
		case "/api/v1/namespaces/" + ClusterNamespace(cluster) + "/pods":
			assert.Equal(test, "app=inference,endpoint=endpoint,workspace=workspace", request.URL.Query().Get("labelSelector"))
			if statusCode != http.StatusOK {
				http.Error(writer, "pod list denied", statusCode)
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
	kubeconfig := makeKubeConfig("    server: "+server.URL, "    {}")
	cluster.Spec = &v1.ClusterSpec{Config: &v1.ClusterConfig{KubernetesConfig: &v1.KubernetesClusterConfig{
		Kubeconfig: base64.StdEncoding.EncodeToString([]byte(kubeconfig)),
	}}}
	return cluster
}
