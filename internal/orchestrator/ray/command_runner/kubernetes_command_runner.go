package command_runner

import (
	"bytes"
	"context"

	"github.com/pkg/errors"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/klog"
	"k8s.io/kubectl/pkg/scheme"
)

type KubernetesCommandRunner struct {
	kubeconfig string
	container  string
	podName    string
	namespace  string
}

func NewKubernetesCommandRunner(kubeconfig, podName string, namespace string, container string) *KubernetesCommandRunner {
	return &KubernetesCommandRunner{
		kubeconfig: kubeconfig,
		container:  container,
		podName:    podName,
		namespace:  namespace,
	}
}

func (k *KubernetesCommandRunner) Run(ctx context.Context, command string) (string, error) {
	klog.V(4).Infof("Pod %s running command: %s", k.podName, command)

	restConfig, err := clientcmd.RESTConfigFromKubeConfig([]byte(k.kubeconfig))
	if err != nil {
		return "", errors.Wrap(err, "failed to create REST config")
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return "", errors.Wrap(err, "failed to create Kubernetes client")
	}

	req := clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(k.podName).
		Namespace(k.namespace).
		SubResource("exec").
		VersionedParams(&v1.PodExecOptions{
			Container: k.container,
			Stdin:     false,
			Stdout:    true,
			Stderr:    true,
			TTY:       false,
			Command:   []string{"sh", "-c", command},
		}, runtime.NewParameterCodec(scheme.Scheme))

	executor, err := remotecommand.NewSPDYExecutor(restConfig, "POST", req.URL())
	if err != nil {
		return "", errors.Wrap(err, "failed to create SPDY executor")
	}

	var stdout, stderr bytes.Buffer
	err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:  nil,
		Stdout: &stdout,
		Stderr: &stderr,
	})

	if err != nil {
		return "", errors.Wrap(err, "failed to execute command")
	}

	if stdout.Len() > 0 {
		klog.V(4).Infof("Command output: %s", stdout.String())
	}

	return stdout.String(), nil
}
