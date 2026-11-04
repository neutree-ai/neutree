package bench

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/kubectl/pkg/scheme"

	v1 "github.com/neutree-ai/neutree/api/v1"
	internalutil "github.com/neutree-ai/neutree/internal/util"
)

type kubernetesTarget struct {
	PodName   string
	Container string
}

func selectKubernetesTarget(pods []corev1.Pod) (kubernetesTarget, error) {
	candidates := make([]corev1.Pod, 0, len(pods))

	for _, pod := range pods {
		// A terminating Pod keeps its Running phase and Ready condition until
		// SIGTERM, so only the deletion timestamp tells it apart from a target
		// that stays up for the whole benchmark.
		if pod.Status.Phase == corev1.PodRunning && podReady(pod) && pod.DeletionTimestamp == nil {
			candidates = append(candidates, pod)
		}
	}

	sort.Slice(candidates, func(left, right int) bool {
		return candidates[left].Name < candidates[right].Name
	})

	var firstErr error

	for _, pod := range candidates {
		container, err := vllmContainer(pod.Spec.Containers)
		if err == nil {
			return kubernetesTarget{
				PodName:   pod.Name,
				Container: container,
			}, nil
		}

		if firstErr == nil {
			firstErr = fmt.Errorf("pod %s: %w", pod.Name, err)
		}
	}

	if firstErr != nil {
		return kubernetesTarget{}, firstErr
	}

	return kubernetesTarget{}, fmt.Errorf("no Ready inference Pod found")
}

func vllmContainer(containers []corev1.Container) (string, error) {
	if len(containers) == 0 {
		return "", fmt.Errorf("Pod has no regular containers")
	}

	for _, container := range containers {
		if container.Name == "vllm" {
			return container.Name, nil
		}
	}

	if len(containers) != 1 {
		return "", fmt.Errorf("vllm container not found")
	}

	return containers[0].Name, nil
}

// podReady reports whether Kubernetes considers the Pod ready. A container that
// is starting or crash-looping keeps the Pod phase Running while the Readiness
// condition is false, and exec into such a Pod fails the run before any case.
func podReady(pod corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}

	return false
}

func kubernetesRouterBaseURL(namespace, workspace, endpoint string) (string, error) {
	if strings.TrimSpace(namespace) == "" {
		return "", fmt.Errorf("cluster namespace is required")
	}

	if strings.TrimSpace(workspace) == "" || strings.TrimSpace(endpoint) == "" {
		return "", fmt.Errorf("workspace and endpoint are required")
	}

	return (&url.URL{
		Scheme: "http",
		Host:   "router-service." + namespace + ".svc.cluster.local:8000",
		Path:   "/" + workspace + "/" + endpoint,
	}).String(), nil
}

func resolveKubernetesTarget(ctx context.Context, _ *v1.Cluster, credentialCluster *v1.Cluster, endpoint *v1.Endpoint) (BenchTarget, error) {
	if credentialCluster == nil {
		return BenchTarget{}, fmt.Errorf("Kubernetes cluster credentials are unavailable")
	}

	kubeconfig, err := internalutil.GetKubeConfigFromCluster(credentialCluster)
	if err != nil {
		return BenchTarget{}, fmt.Errorf("get Kubernetes kubeconfig: %w", err)
	}

	restConfig, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfig))
	if err != nil {
		return BenchTarget{}, fmt.Errorf("create Kubernetes REST config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return BenchTarget{}, fmt.Errorf("create Kubernetes client: %w", err)
	}

	// Pod listing and exec must agree on the namespace, and the credentials
	// response carries the same Cluster record the listing resolves.
	namespace := internalutil.ClusterNamespace(credentialCluster)

	pods, err := internalutil.GetEndpointPods(ctx, credentialCluster, endpoint)
	if err != nil {
		return BenchTarget{}, fmt.Errorf("list inference Pods: %w", err)
	}

	selected, err := selectKubernetesTarget(pods)
	if err != nil {
		return BenchTarget{}, err
	}

	baseURL, err := kubernetesRouterBaseURL(namespace, endpoint.Metadata.Workspace, endpoint.Metadata.Name)
	if err != nil {
		return BenchTarget{}, err
	}

	return BenchTarget{
		Platform:  "kubernetes",
		RuntimeID: "pod/" + selected.PodName + ":" + selected.Container,
		BaseURL:   baseURL,
		runner: &kubernetesRemoteRunner{
			restConfig: restConfig,
			clientset:  clientset,
			namespace:  namespace,
			podName:    selected.PodName,
			container:  selected.Container,
		},
	}, nil
}

type kubernetesRemoteRunner struct {
	restConfig *rest.Config
	clientset  *kubernetes.Clientset
	namespace  string
	podName    string
	container  string
}

func (r *kubernetesRemoteRunner) Run(ctx context.Context, command string, stdout, stderr io.Writer) error {
	req := r.clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(r.podName).
		Namespace(r.namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: r.container,
			Stdout:    true,
			Stderr:    true,
			TTY:       false,
			Command:   []string{"sh", "-c", command},
		}, runtime.NewParameterCodec(scheme.Scheme))

	executor, err := remotecommand.NewSPDYExecutor(r.restConfig, "POST", req.URL())
	if err != nil {
		return fmt.Errorf("create Kubernetes exec stream: %w", err)
	}

	if err := executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: stdout,
		Stderr: stderr,
	}); err != nil {
		return fmt.Errorf("exec in Pod %s/%s: %w", r.namespace, r.podName, err)
	}

	return nil
}
