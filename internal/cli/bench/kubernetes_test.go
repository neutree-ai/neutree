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

func TestSelectKubernetesTargetSortsPodsAndFindsVLLMTokenizer(t *testing.T) {
	pods := []corev1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "endpoint-b"},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Name:    "vllm",
				Command: []string{"vllm"},
				Args:    []string{"serve", "/models/second"},
			}}},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "endpoint-a"},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Name:    "vllm",
				Command: []string{"vllm", "serve", "/models/first"},
			}}},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "endpoint-pending"},
			Status:     corev1.PodStatus{Phase: corev1.PodPending},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "vllm"}}},
		},
	}

	target, err := selectKubernetesTarget(pods)
	require.NoError(t, err)
	require.Equal(t, "endpoint-a", target.PodName)
	require.Equal(t, "vllm", target.Container)
	require.Equal(t, "/models/first", target.Tokenizer.Path)
}

func TestTokenizerOptionsFromContainerInheritsServeFlags(t *testing.T) {
	options, err := tokenizerOptionsFromContainer(corev1.Container{
		Name: "vllm",
		Command: []string{
			"vllm", "serve", "/models/chat",
			"--tokenizer", "/models/custom-tokenizer",
			"--tokenizer-mode", "slow",
			"--trust-remote-code",
		},
	})
	require.NoError(t, err)
	require.Equal(t, tokenizerOptions{
		Path:            "/models/custom-tokenizer",
		Mode:            "slow",
		TrustRemoteCode: true,
	}, options)
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
