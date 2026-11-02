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

func TestSelectKubernetesTargetSortsPodsAndFindsVLLMContainer(t *testing.T) {
	pods := []corev1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "endpoint-b"},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning, Conditions: readyConditions()},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "vllm"}}},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "endpoint-a"},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning, Conditions: readyConditions()},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "vllm"}}},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "endpoint-pending"},
			Status:     corev1.PodStatus{Phase: corev1.PodPending, Conditions: readyConditions()},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "vllm"}}},
		},
	}

	target, err := selectKubernetesTarget(pods)
	require.NoError(t, err)
	require.Equal(t, "endpoint-a", target.PodName)
	require.Equal(t, "vllm", target.Container)
}

// A Pod stays in the Running phase while its container is starting or
// crash-looping. Exec into it fails before any case runs, so selection has to
// look at the Readiness condition too.
func TestSelectKubernetesTargetSkipsRunningButNotReadyPods(t *testing.T) {
	pods := []corev1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "endpoint-a"},
			Status: corev1.PodStatus{
				Phase:      corev1.PodRunning,
				Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}},
			},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "vllm"}}},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "endpoint-b"},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning, Conditions: readyConditions()},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "vllm"}}},
		},
	}

	target, err := selectKubernetesTarget(pods)
	require.NoError(t, err)
	require.Equal(t, "endpoint-b", target.PodName)
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
