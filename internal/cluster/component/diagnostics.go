package component

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	maxDiagnosticObjects = 5
	maxDiagnosticBytes   = 8 * 1024
)

// DeploymentDiagnostics summarizes Kubernetes status fields that help explain why a component is not ready.
func DeploymentDiagnostics(ctx context.Context, ctrlClient client.Client, namespace, name string, podLabels map[string]string) []string {
	diagnostics := []string{}

	deployment := &appsv1.Deployment{}
	if err := ctrlClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, deployment); err != nil {
		return append(diagnostics, fmt.Sprintf("deployment/%s: get failed: %v", name, err))
	}

	diagnostics = append(diagnostics, formatDeployment(deployment))
	diagnostics = append(diagnostics, podDiagnostics(ctx, ctrlClient, namespace, podLabels)...)
	diagnostics = append(diagnostics, eventDiagnostics(ctx, ctrlClient, namespace, "Deployment", name, deployment.UID)...)

	return diagnostics
}

// ServiceDiagnostics summarizes Kubernetes service status and recent service events.
func ServiceDiagnostics(ctx context.Context, ctrlClient client.Client, namespace, name string) []string {
	service := &corev1.Service{}
	if err := ctrlClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, service); err != nil {
		return []string{fmt.Sprintf("service/%s: get failed: %v", name, err)}
	}

	diagnostics := []string{formatService(service)}
	diagnostics = append(diagnostics, eventDiagnostics(ctx, ctrlClient, namespace, "Service", name, service.UID)...)

	return diagnostics
}

// FormatStatusWithDiagnostics appends diagnostic lines to a concise status string and bounds the result.
func FormatStatusWithDiagnostics(base string, diagnostics []string) string {
	if len(diagnostics) == 0 {
		return base
	}

	return truncateDiagnosticMessage(base + "\nDiagnostics:\n" + strings.Join(diagnostics, "\n"))
}

func formatDeployment(deployment *appsv1.Deployment) string {
	desired := int32(0)
	if deployment.Spec.Replicas != nil {
		desired = *deployment.Spec.Replicas
	}

	parts := []string{
		fmt.Sprintf("deployment/%s: desired=%d, updated=%d, ready=%d, available=%d, observedGeneration=%d, generation=%d",
			deployment.Name,
			desired,
			deployment.Status.UpdatedReplicas,
			deployment.Status.ReadyReplicas,
			deployment.Status.AvailableReplicas,
			deployment.Status.ObservedGeneration,
			deployment.Generation,
		),
	}

	for _, condition := range deployment.Status.Conditions {
		parts = append(parts, fmt.Sprintf("condition %s=%s reason=%s message=%q",
			condition.Type,
			condition.Status,
			condition.Reason,
			condition.Message,
		))
	}

	return strings.Join(parts, ", ")
}

func podDiagnostics(ctx context.Context, ctrlClient client.Client, namespace string, labels map[string]string) []string {
	pods := &corev1.PodList{}
	if err := ctrlClient.List(ctx, pods, client.InNamespace(namespace), client.MatchingLabels(labels)); err != nil {
		return []string{fmt.Sprintf("pods: list failed: %v", err)}
	}

	sort.Slice(pods.Items, func(i, j int) bool {
		return pods.Items[i].Name < pods.Items[j].Name
	})

	diagnostics := []string{}
	podCount := 0

	for i := range pods.Items {
		pod := &pods.Items[i]
		if !isPodDiagnosticRelevant(pod) {
			continue
		}

		if podCount >= maxDiagnosticObjects {
			break
		}

		diagnostics = append(diagnostics, formatPod(pod))
		diagnostics = append(diagnostics, eventDiagnostics(ctx, ctrlClient, namespace, "Pod", pod.Name, pod.UID)...)
		podCount++
	}

	return diagnostics
}

func isPodDiagnosticRelevant(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodRunning && pod.Status.Phase != corev1.PodSucceeded {
		return true
	}

	for _, status := range pod.Status.ContainerStatuses {
		if !status.Ready || status.State.Waiting != nil || status.State.Terminated != nil {
			return true
		}
	}

	return false
}

func formatPod(pod *corev1.Pod) string {
	parts := []string{fmt.Sprintf("pod/%s: phase=%s", pod.Name, pod.Status.Phase)}
	if pod.Status.Reason != "" {
		parts = append(parts, fmt.Sprintf("reason=%s", pod.Status.Reason))
	}

	if pod.Status.Message != "" {
		parts = append(parts, fmt.Sprintf("message=%q", pod.Status.Message))
	}

	for _, status := range pod.Status.ContainerStatuses {
		switch {
		case status.State.Waiting != nil:
			parts = append(parts, fmt.Sprintf("container/%s waiting reason=%s message=%q",
				status.Name,
				status.State.Waiting.Reason,
				status.State.Waiting.Message,
			))
		case status.State.Terminated != nil:
			parts = append(parts, fmt.Sprintf("container/%s terminated reason=%s exitCode=%d message=%q",
				status.Name,
				status.State.Terminated.Reason,
				status.State.Terminated.ExitCode,
				status.State.Terminated.Message,
			))
		case !status.Ready:
			parts = append(parts, fmt.Sprintf("container/%s ready=false", status.Name))
		}
	}

	return strings.Join(parts, ", ")
}

func formatService(service *corev1.Service) string {
	ports := []string{}

	for _, port := range service.Spec.Ports {
		targetPort := ""
		if port.TargetPort.String() != "0" {
			targetPort = "->" + port.TargetPort.String()
		}

		nodePort := ""
		if port.NodePort != 0 {
			nodePort = fmt.Sprintf(",nodePort=%d", port.NodePort)
		}

		portSummary := fmt.Sprintf("%s:%d/%s%s%s", port.Name, port.Port, port.Protocol, targetPort, nodePort)
		ports = append(ports, portSummary)
	}

	if len(ports) == 0 {
		ports = append(ports, "<none>")
	}

	ingress := []string{}

	for _, item := range service.Status.LoadBalancer.Ingress {
		switch {
		case item.IP != "":
			ingress = append(ingress, item.IP)
		case item.Hostname != "":
			ingress = append(ingress, item.Hostname)
		}
	}

	if len(ingress) == 0 {
		ingress = append(ingress, "<empty>")
	}

	return fmt.Sprintf("service/%s: type=%s, clusterIP=%s, ports=%s, ingress=%s",
		service.Name,
		service.Spec.Type,
		service.Spec.ClusterIP,
		strings.Join(ports, ","),
		strings.Join(ingress, ","),
	)
}

func eventDiagnostics(ctx context.Context, ctrlClient client.Client, namespace, kind, name string, uid types.UID) []string {
	events := &corev1.EventList{}
	if err := ctrlClient.List(ctx, events, client.InNamespace(namespace)); err != nil {
		return []string{fmt.Sprintf("events/%s: list failed: %v", name, err)}
	}

	matched := []corev1.Event{}

	for _, event := range events.Items {
		if event.InvolvedObject.Kind != kind || event.InvolvedObject.Name != name {
			continue
		}

		if uid != "" && event.InvolvedObject.UID != uid {
			continue
		}

		matched = append(matched, event)
	}

	sort.Slice(matched, func(i, j int) bool {
		if matched[i].Type != matched[j].Type {
			return matched[i].Type == corev1.EventTypeWarning
		}

		return matched[i].LastTimestamp.Time.After(matched[j].LastTimestamp.Time)
	})

	if len(matched) > maxDiagnosticObjects {
		matched = matched[:maxDiagnosticObjects]
	}

	diagnostics := []string{}
	for _, event := range matched {
		diagnostics = append(diagnostics, fmt.Sprintf("event/%s: %s %s - %s",
			name,
			event.Type,
			event.Reason,
			event.Message,
		))
	}

	return diagnostics
}

func truncateDiagnosticMessage(message string) string {
	if len(message) <= maxDiagnosticBytes {
		return message
	}

	const suffix = "\n... truncated"

	cut := maxDiagnosticBytes - len(suffix)
	for cut > 0 && !utf8.RuneStart(message[cut]) {
		cut--
	}

	return message[:cut] + suffix
}
