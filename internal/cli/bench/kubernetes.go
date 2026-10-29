package bench

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
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
	Tokenizer tokenizerOptions
}

func selectKubernetesTarget(pods []corev1.Pod) (kubernetesTarget, error) {
	candidates := make([]corev1.Pod, 0, len(pods))

	for _, pod := range pods {
		if pod.Status.Phase == corev1.PodRunning {
			candidates = append(candidates, pod)
		}
	}

	sort.Slice(candidates, func(left, right int) bool {
		return candidates[left].Name < candidates[right].Name
	})

	var firstErr error

	for _, pod := range candidates {
		container, tokenizer, err := vllmContainerAndTokenizer(pod.Spec.Containers)
		if err == nil {
			return kubernetesTarget{
				PodName:   pod.Name,
				Container: container,
				Tokenizer: tokenizer,
			}, nil
		}

		if firstErr == nil {
			firstErr = fmt.Errorf("pod %s: %w", pod.Name, err)
		}
	}

	if firstErr != nil {
		return kubernetesTarget{}, firstErr
	}

	return kubernetesTarget{}, fmt.Errorf("no Running inference Pod found")
}

func vllmContainerAndTokenizer(containers []corev1.Container) (string, tokenizerOptions, error) {
	if len(containers) == 0 {
		return "", tokenizerOptions{}, fmt.Errorf("Pod has no regular containers")
	}

	selected := -1

	for index, container := range containers {
		if container.Name == "vllm" {
			selected = index
			break
		}
	}

	if selected < 0 {
		if len(containers) != 1 {
			return "", tokenizerOptions{}, fmt.Errorf("vllm container not found")
		}

		selected = 0
	}

	tokenizer, err := tokenizerOptionsFromContainer(containers[selected])
	if err != nil {
		return "", tokenizerOptions{}, err
	}

	return containers[selected].Name, tokenizer, nil
}

// tokenizerOptionsFromContainer reads the tokenizer flags from the rendered
// `vllm serve` arguments so the benchmark tokenizes like the serving engine.
func tokenizerOptionsFromContainer(container corev1.Container) (tokenizerOptions, error) {
	args := make([]string, 0, len(container.Command)+len(container.Args))
	args = append(args, container.Command...)
	args = append(args, container.Args...)

	modelIndex := -1

	for index := 0; index+2 < len(args); index++ {
		if strings.TrimSpace(args[index]) == "vllm" &&
			strings.TrimSpace(args[index+1]) == "serve" &&
			strings.TrimSpace(args[index+2]) != "" {
			modelIndex = index + 2
			break
		}
	}

	if modelIndex < 0 {
		return tokenizerOptions{}, fmt.Errorf("tokenizer path is unavailable from vllm serve arguments")
	}

	options := tokenizerOptions{Path: args[modelIndex]}

	for index := modelIndex + 1; index < len(args); index++ {
		name, value, hasValue := splitServeArgument(args[index])

		switch name {
		case "tokenizer":
			if !hasValue && index+1 < len(args) {
				index++
				value = args[index]
			}

			if value != "" {
				options.Path = value
			}
		case "tokenizer-mode":
			if !hasValue && index+1 < len(args) {
				index++
				value = args[index]
			}

			options.Mode = value
		case "trust-remote-code":
			options.TrustRemoteCode = true
		}
	}

	return options, nil
}

// splitServeArgument splits one engine argument into its flag name and value.
// The serving template renders flags as separate `--flag value` pairs; the
// `--flag=value` form is also accepted.
func splitServeArgument(argument string) (string, string, bool) {
	if !strings.HasPrefix(argument, "--") {
		return "", "", false
	}

	name, value, found := strings.Cut(strings.TrimPrefix(argument, "--"), "=")

	return name, value, found
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

func resolveKubernetesTarget(ctx context.Context, cluster, credentialCluster *v1.Cluster, endpoint *v1.Endpoint) (BenchTarget, error) {
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

	namespace := internalutil.ClusterNamespace(cluster)
	selector := labels.Set(map[string]string{
		"app":      "inference",
		"endpoint": endpoint.Metadata.Name,
	}).String()

	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return BenchTarget{}, fmt.Errorf("list inference Pods: %w", err)
	}

	selected, err := selectKubernetesTarget(pods.Items)
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
		Tokenizer: selected.Tokenizer,
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
