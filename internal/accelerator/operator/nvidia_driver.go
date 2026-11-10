package operator

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/neutree-ai/neutree/pkg/accelerator"
)

func nvidiaDriverPods(ctx context.Context, reader client.Reader) ([]corev1.Pod, error) {
	daemonSets := &appsv1.DaemonSetList{}
	if err := reader.List(ctx, daemonSets); err != nil {
		return nil, fmt.Errorf("list NVIDIA driver DaemonSets: %w", err)
	}
	var drivers []appsv1.DaemonSet

	for _, ds := range daemonSets.Items {
		if ds.DeletionTimestamp == nil && ds.Spec.Template.Spec.NodeSelector[accelerator.NvidiaDriverDeployLabel] == "true" {
			drivers = append(drivers, ds)
		}
	}

	if len(drivers) == 0 {
		return nil, nil
	}

	pods := &corev1.PodList{}
	if err := reader.List(ctx, pods); err != nil {
		return nil, fmt.Errorf("list NVIDIA driver Pods: %w", err)
	}
	var result []corev1.Pod

	for _, pod := range pods.Items {
		if pod.DeletionTimestamp != nil {
			continue
		}

		for _, ds := range drivers {
			if pod.Namespace != ds.Namespace {
				continue
			}

			for _, owner := range pod.OwnerReferences {
				if owner.Controller != nil && *owner.Controller && owner.Kind == "DaemonSet" && owner.Name == ds.Name && owner.UID == ds.UID {
					result = append(result, pod)
				}
			}
		}
	}

	return result, nil
}

func driverPodForNode(pods []corev1.Pod, node string) *corev1.Pod {
	var latest *corev1.Pod

	for i := range pods {
		pod := &pods[i]
		if pod.Spec.NodeName != node {
			continue
		}

		if latest == nil || pod.CreationTimestamp.After(latest.CreationTimestamp.Time) {
			latest = pod
		}
	}

	return latest
}

func driverPodFailure(pod *corev1.Pod) string {
	for _, status := range pod.Status.InitContainerStatuses {
		if status.State.Waiting != nil {
			return "Init:" + status.State.Waiting.Reason
		}

		if status.State.Terminated != nil && status.State.Terminated.ExitCode != 0 {
			return "Init:" + status.State.Terminated.Reason
		}
	}

	for _, status := range pod.Status.ContainerStatuses {
		if status.State.Waiting != nil && status.State.Waiting.Reason != "" {
			return status.State.Waiting.Reason
		}

		if status.State.Terminated != nil && status.State.Terminated.Reason != "" {
			return status.State.Terminated.Reason
		}
	}

	if pod.Status.Phase == "" {
		return string(corev1.PodUnknown)
	}

	if pod.Status.Phase != corev1.PodRunning {
		return string(pod.Status.Phase)
	}

	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			return ""
		}
	}

	return "Pod is not Ready"
}
