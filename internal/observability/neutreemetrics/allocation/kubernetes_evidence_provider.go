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
	if p.Client == nil || p.NodeName == "" || p.PodResources == nil {
		return adapter.KubernetesEvidence{}, nil
	}

	podResources, err := p.PodResources.ListPodResources(ctx)
	if err != nil {
		return adapter.KubernetesEvidence{}, err
	}

	pods, err := p.localEndpointPods(ctx)
	if err != nil {
		return adapter.KubernetesEvidence{}, err
	}

	nodeLabels, nodeAnnotations, err := p.localNodeMetadata(ctx)
	if err != nil {
		return adapter.KubernetesEvidence{}, err
	}

	evidence := adapter.KubernetesEvidence{
		AllocationAvailable: true,
		PodResources:        podResources,
		EndpointPods:        endpointPodEvidence(pods),
		NodeLabels:          nodeLabels,
		NodeAnnotations:     nodeAnnotations,
	}

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

func (p KubernetesAllocationProvider) localNodeMetadata(ctx context.Context) (map[string]string, map[string]string, error) {
	node := &corev1.Node{}
	if err := p.Client.Get(ctx, client.ObjectKey{Name: p.NodeName}, node); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil, nil
		}

		return nil, nil, err
	}

	return node.Labels, node.Annotations, nil
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
