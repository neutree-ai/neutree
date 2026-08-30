package allocation

import (
	"context"
	"sort"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

const endpointPodAppLabelValue = "inference"

// PodResourceLister reads the kubelet's raw PodResources response without
// assigning accelerator resource-name or device-ID semantics.
type PodResourceLister interface {
	ListPodResources(context.Context) ([]adapter.PodResource, error)
}

type PodResourceListerFunc func(context.Context) ([]adapter.PodResource, error)

func (f PodResourceListerFunc) ListPodResources(ctx context.Context) ([]adapter.PodResource, error) {
	return f(ctx)
}

// KubernetesAllocationProvider collects raw, local Kubernetes evidence for a
// selected accelerator adapter. It deliberately does not interpret vendor
// resources, annotations, or device IDs.
type KubernetesAllocationProvider struct {
	Client       client.Client
	NodeName     string
	PodResources PodResourceLister
}

func (p KubernetesAllocationProvider) KubernetesAcceleratorEvidence(
	ctx context.Context,
) (adapter.KubernetesEvidence, error) {
	if p.Client == nil || p.NodeName == "" {
		return adapter.KubernetesEvidence{}, nil
	}

	node, err := p.localNode(ctx)
	if err != nil {
		return adapter.KubernetesEvidence{}, err
	}

	evidence := nodeEvidence(node)
	if p.PodResources == nil {
		return evidence.Clone(), nil
	}

	podResources, err := p.PodResources.ListPodResources(ctx)
	if err != nil {
		return evidence.Clone(), nil
	}

	pods, err := p.localEndpointPods(ctx)
	if err != nil {
		return evidence.Clone(), nil
	}

	evidence.AllocationAvailable = true
	evidence.PodResources = podResources
	evidence.EndpointPods = endpointPodEvidence(pods)

	return evidence.Clone(), nil
}

func (p KubernetesAllocationProvider) localEndpointPods(ctx context.Context) ([]corev1.Pod, error) {
	podList := &corev1.PodList{}
	if err := p.Client.List(
		ctx,
		podList,
		client.MatchingFields{"spec.nodeName": p.NodeName},
		client.MatchingLabels{"app": endpointPodAppLabelValue},
	); err != nil {
		return nil, err
	}

	pods := make([]corev1.Pod, 0)

	for _, pod := range podList.Items {
		if pod.Spec.NodeName != p.NodeName || terminalPodPhase(pod.Status.Phase) {
			continue
		}

		labels := pod.GetLabels()
		if labels["app"] != endpointPodAppLabelValue || labels["endpoint"] == "" {
			continue
		}

		pods = append(pods, pod)
	}

	sort.SliceStable(pods, func(i, j int) bool {
		if pods[i].Namespace != pods[j].Namespace {
			return pods[i].Namespace < pods[j].Namespace
		}

		return pods[i].Name < pods[j].Name
	})

	return pods, nil
}

func (p KubernetesAllocationProvider) localNode(ctx context.Context) (*corev1.Node, error) {
	node := &corev1.Node{}
	if err := p.Client.Get(ctx, client.ObjectKey{Name: p.NodeName}, node); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}

		return nil, err
	}

	return node, nil
}

func nodeEvidence(node *corev1.Node) adapter.KubernetesEvidence {
	if node == nil {
		return adapter.KubernetesEvidence{}
	}

	return adapter.KubernetesEvidence{
		NodeLabels:               node.Labels,
		NodeAnnotations:          node.Annotations,
		NodeAllocatableResources: allocatableResources(node.Status.Allocatable),
	}
}

func allocatableResources(resources corev1.ResourceList) map[string]int64 {
	if len(resources) == 0 {
		return nil
	}

	result := make(map[string]int64, len(resources))
	for name, quantity := range resources {
		result[string(name)] = quantity.Value()
	}

	return result
}

func endpointPodEvidence(pods []corev1.Pod) []adapter.EndpointPodEvidence {
	evidence := make([]adapter.EndpointPodEvidence, 0, len(pods))
	for _, pod := range pods {
		evidence = append(evidence, adapter.EndpointPodEvidence{
			Namespace:   pod.Namespace,
			Name:        pod.Name,
			UID:         string(pod.UID),
			NodeName:    pod.Spec.NodeName,
			Labels:      pod.Labels,
			Annotations: pod.Annotations,
		})
	}

	return evidence
}

func terminalPodPhase(phase corev1.PodPhase) bool {
	return phase == corev1.PodSucceeded || phase == corev1.PodFailed
}
