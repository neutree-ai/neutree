package metrics

import (
	"context"

	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator/resourceparser"
)

func (m *MetricsComponent) nodeAnnotationCleanupScope(ctx context.Context) (map[string]struct{}, error) {
	if len(m.excludedAccelerators) == 0 {
		return nil, nil
	}

	plan, err := m.deploymentPlan(ctx)
	if err != nil {
		return nil, err
	}

	owned := map[types.UID]string{}

	for _, name := range plan.nodeAgentNames() {
		ds := &appsv1.DaemonSet{}
		if err := m.ctrlClient.Get(ctx, client.ObjectKey{Namespace: m.namespace, Name: name}, ds); err != nil {
			if client.IgnoreNotFound(err) != nil {
				return nil, err
			}

			continue
		}

		if ds.UID != "" && ds.Labels[v1.NeutreeClusterLabelKey] == m.cluster.GetName() &&
			ds.Labels[v1.NeutreeClusterWorkspaceLabelKey] == m.cluster.GetWorkspace() && ds.Labels["neutree.ai/accelerator-operator"] == "" {
			owned[ds.UID] = ds.Name
		}
	}
	// Capture current bindings before deleting the workloads. Unknown or foreign
	// NodeAgents protect their nodes; the legacy path cannot attribute that data.
	pods := &corev1.PodList{}
	if err := m.ctrlClient.List(ctx, pods, client.MatchingLabels{"neutree.ai/metrics-target": "node-agent"}); err != nil {
		return nil, err
	}

	nodes, protected := map[string]struct{}{}, map[string]struct{}{}

	for _, pod := range pods.Items {
		if pod.Spec.NodeName == "" {
			continue
		}

		owner := metav1.GetControllerOf(&pod)
		if pod.Namespace == m.namespace && pod.Labels["neutree.ai/accelerator-operator"] == "" &&
			owner != nil && owner.Kind == daemonSetKind && owner.UID != "" && owned[owner.UID] == owner.Name {
			nodes[pod.Spec.NodeName] = struct{}{}
		} else {
			protected[pod.Spec.NodeName] = struct{}{}
		}
	}

	for name := range protected {
		delete(nodes, name)
	}

	return nodes, nil
}

func (m *MetricsComponent) cleanupNodeAnnotations(ctx context.Context, nodes map[string]struct{}) error {
	nodeList := &corev1.NodeList{}
	if err := m.ctrlClient.List(ctx, nodeList); err != nil {
		return errors.Wrap(err, "failed to list nodes")
	}

	for _, item := range nodeList.Items {
		if nodes != nil {
			if _, owned := nodes[item.Name]; !owned {
				continue
			}
		}

		if !hasNeutreeAcceleratorDevicesAnnotation(item.Annotations) {
			continue
		}

		node := &corev1.Node{}
		if err := m.ctrlClient.Get(ctx, types.NamespacedName{Name: item.Name}, node); err != nil {
			return errors.Wrapf(err, "failed to get node %s", item.Name)
		}

		if !cleanupNeutreeAcceleratorDevicesAnnotation(node) {
			continue
		}

		if err := m.ctrlClient.Update(ctx, node); err != nil {
			return errors.Wrapf(err, "failed to patch node %s", item.Name)
		}
	}

	return nil
}

func cleanupNeutreeAcceleratorDevicesAnnotation(node *corev1.Node) bool {
	if !hasNeutreeAcceleratorDevicesAnnotation(node.Annotations) {
		return false
	}

	delete(node.Annotations, resourceparser.NeutreeAcceleratorDevicesAnnotation)

	return true
}

func hasNeutreeAcceleratorDevicesAnnotation(annotations map[string]string) bool {
	_, ok := annotations[resourceparser.NeutreeAcceleratorDevicesAnnotation]

	return ok
}
