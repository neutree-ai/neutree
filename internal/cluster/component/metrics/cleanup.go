package metrics

import (
	"context"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/neutree-ai/neutree/internal/accelerator/resourceparser"
)

func (m *MetricsComponent) cleanupNodeAnnotations(ctx context.Context) error {
	nodeList := &corev1.NodeList{}
	if err := m.ctrlClient.List(ctx, nodeList); err != nil {
		return errors.Wrap(err, "failed to list nodes")
	}

	for _, item := range nodeList.Items {
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
