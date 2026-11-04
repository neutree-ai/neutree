package bench

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// A Pod stays in the Running phase while its container is starting or
// crash-looping. Exec into it fails before any case runs, so selection has to
// look at the Readiness condition too.
func TestSelectKubernetesTarget(t *testing.T) {
	for _, tc := range []struct {
		name string
		pods []corev1.Pod
		want kubernetesTarget
	}{
		{
			name: "sorts ready pods by name",
			pods: []corev1.Pod{
				endpointPod("endpoint-b", corev1.PodRunning, true),
				endpointPod("endpoint-a", corev1.PodRunning, true),
				endpointPod("endpoint-pending", corev1.PodPending, true),
			},
			want: kubernetesTarget{PodName: "endpoint-a", Container: "vllm"},
		},
		{
			name: "skips running pods that are not ready",
			pods: []corev1.Pod{
				endpointPod("endpoint-a", corev1.PodRunning, false),
				endpointPod("endpoint-b", corev1.PodRunning, true),
			},
			want: kubernetesTarget{PodName: "endpoint-b", Container: "vllm"},
		},
		{
			name: "skips terminating pods",
			pods: []corev1.Pod{
				terminatingPod("endpoint-a"),
				endpointPod("endpoint-b", corev1.PodRunning, true),
			},
			want: kubernetesTarget{PodName: "endpoint-b", Container: "vllm"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target, err := selectKubernetesTarget(tc.pods)
			require.NoError(t, err)
			require.Equal(t, tc.want, target)
		})
	}
}

func endpointPod(name string, phase corev1.PodPhase, ready bool) corev1.Pod {
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status:     corev1.PodStatus{Phase: phase},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "vllm"}}},
	}
	if ready {
		pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	}

	return pod
}

func terminatingPod(name string) corev1.Pod {
	pod := endpointPod(name, corev1.PodRunning, true)
	now := metav1.Now()
	pod.DeletionTimestamp = &now

	return pod
}

func readyConditions() []corev1.PodCondition {
	return []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
}

func TestKubernetesRouterBaseURL(t *testing.T) {
	url, err := kubernetesRouterBaseURL("neutree-cluster-a", "team-a", "chat")
	require.NoError(t, err)
	require.Equal(t,
		"http://router-service.neutree-cluster-a.svc.cluster.local:8000/team-a/chat",
		url,
	)
}

func TestKubernetesRemoteRunnerReturnsExecStreamFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, http.MethodPost, request.Method)
		require.Equal(t, "/api/v1/namespaces/default/pods/chat-a/exec", request.URL.Path)
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	restConfig := &rest.Config{Host: server.URL}
	clientset, err := kubernetes.NewForConfig(restConfig)
	require.NoError(t, err)

	runner := &kubernetesRemoteRunner{
		restConfig: restConfig,
		clientset:  clientset,
		namespace:  "default",
		podName:    "chat-a",
		container:  "vllm",
	}
	err = runner.Run(context.Background(), "echo hello", &bytes.Buffer{}, &bytes.Buffer{})
	require.ErrorContains(t, err, "exec in Pod default/chat-a")
}
