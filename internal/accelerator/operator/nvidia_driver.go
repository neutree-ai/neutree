package operator

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/neutree-ai/neutree/pkg/accelerator"
)

func nvidiaDriverPods(ctx context.Context, reader client.Reader) ([]corev1.Pod, error) {
	pods := &corev1.PodList{}
	if err := reader.List(ctx, pods, client.MatchingLabels{
		accelerator.NvidiaDriverAppLabel:       accelerator.NvidiaDriverAppValue,
		accelerator.NvidiaDriverComponentLabel: accelerator.NvidiaDriverComponentValue,
	}); err != nil {
		return nil, fmt.Errorf("list NVIDIA driver Pods: %w", err)
	}
	var result []corev1.Pod

	for _, pod := range pods.Items {
		if pod.DeletionTimestamp != nil || pod.Spec.NodeName == "" {
			continue
		}

		result = append(result, pod)
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

		if latest == nil {
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
