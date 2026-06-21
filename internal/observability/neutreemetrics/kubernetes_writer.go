package neutreemetrics

import (
	"context"
	"encoding/json"
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator/resourceparser"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type KubernetesAnnotationWriter struct {
	Client   client.Client
	NodeName string
}

func (w *KubernetesAnnotationWriter) Write(ctx context.Context, snapshot *NodeSnapshot) error {
	if w == nil || w.Client == nil || w.NodeName == "" || snapshot == nil {
		return nil
	}

	if err := w.writeNodeDevices(ctx, snapshot.Accelerator.Devices); err != nil {
		return err
	}

	return w.writePodAllocations(ctx, snapshot.Allocations)
}

func (w *KubernetesAnnotationWriter) writeNodeDevices(
	ctx context.Context,
	devices []v1.StaticNodeAcceleratorDeviceStatus,
) error {
	node := &corev1.Node{}
	if err := w.Client.Get(ctx, client.ObjectKey{Name: w.NodeName}, node); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}

		return fmt.Errorf("get local node %s: %w", w.NodeName, err)
	}

	original := node.DeepCopy()
	annotations := copyAnnotations(node.Annotations)
	value, err := json.Marshal(kubernetesDeviceAnnotations(devices))
	if err != nil {
		return err
	}
	annotations[resourceparser.NeutreeAcceleratorDevicesAnnotation] = string(value)
	node.Annotations = annotations

	return w.Client.Patch(ctx, node, client.MergeFrom(original))
}

func (w *KubernetesAnnotationWriter) writePodAllocations(
	ctx context.Context,
	allocations []v1.StaticNodeAllocationStatus,
) error {
	podList := &corev1.PodList{}
	if err := w.Client.List(ctx, podList); err != nil {
		return fmt.Errorf("list pods for node %s: %w", w.NodeName, err)
	}

	allocationsByPod := map[client.ObjectKey][]v1.StaticNodeAllocationStatus{}
	localPods := make([]client.ObjectKey, 0)
	for _, pod := range podList.Items {
		if pod.Spec.NodeName != w.NodeName {
			continue
		}

		key := client.ObjectKey{Namespace: pod.Namespace, Name: pod.Name}
		localPods = append(localPods, key)

		for _, allocation := range allocations {
			if !allocationMatchesPod(allocation, pod) {
				continue
			}

			allocationsByPod[key] = append(allocationsByPod[key], allocation)
		}
	}

	for _, key := range localPods {
		pod := &corev1.Pod{}
		if err := w.Client.Get(ctx, key, pod); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}

			return fmt.Errorf("get pod %s/%s: %w", key.Namespace, key.Name, err)
		}

		original := pod.DeepCopy()
		annotations := copyAnnotations(pod.Annotations)
		podAllocations := allocationsByPod[key]
		if len(podAllocations) == 0 {
			if _, exists := annotations[resourceparser.NeutreeAcceleratorAllocationsAnnotation]; !exists {
				continue
			}

			delete(annotations, resourceparser.NeutreeAcceleratorAllocationsAnnotation)
		} else {
			value, err := json.Marshal(kubernetesAllocationAnnotations(podAllocations))
			if err != nil {
				return err
			}
			annotations[resourceparser.NeutreeAcceleratorAllocationsAnnotation] = string(value)
		}

		pod.Annotations = annotations

		if err := w.Client.Patch(ctx, pod, client.MergeFrom(original)); err != nil {
			return fmt.Errorf("patch pod %s/%s accelerator allocations: %w", key.Namespace, key.Name, err)
		}
	}

	return nil
}

func copyAnnotations(input map[string]string) map[string]string {
	output := map[string]string{}
	for key, value := range input {
		output[key] = value
	}

	return output
}

func allocationMatchesPod(allocation v1.StaticNodeAllocationStatus, pod corev1.Pod) bool {
	candidates := []string{allocation.ReplicaID, allocation.InstanceID, allocation.RuntimeID}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if candidate == pod.Name || candidate == string(pod.UID) {
			return true
		}
	}

	return false
}

type kubernetesDeviceAnnotation struct {
	ID           string `json:"id,omitempty"`
	UUID         string `json:"uuid,omitempty"`
	ProductName  string `json:"product_name,omitempty"`
	ProductModel string `json:"product_model,omitempty"`
	MinorNumber  int    `json:"minor_number,omitempty"`
	MemoryMiB    int64  `json:"memory_mib,omitempty"`
	Healthy      bool   `json:"healthy,omitempty"`
}

type kubernetesAllocationAnnotation struct {
	UUID      string `json:"uuid,omitempty"`
	Product   string `json:"product,omitempty"`
	NodeID    string `json:"node_id,omitempty"`
	MemoryMiB int64  `json:"memory_mib,omitempty"`
	CoreUnits int64  `json:"core_units,omitempty"`
}

func kubernetesDeviceAnnotations(devices []v1.StaticNodeAcceleratorDeviceStatus) []kubernetesDeviceAnnotation {
	result := make([]kubernetesDeviceAnnotation, 0, len(devices))
	for _, device := range devices {
		if device.UUID == "" {
			continue
		}

		result = append(result, kubernetesDeviceAnnotation{
			ID:           device.ID,
			UUID:         device.UUID,
			ProductName:  device.ProductName,
			ProductModel: device.ProductModel,
			MinorNumber:  device.MinorNumber,
			MemoryMiB:    device.MemoryMiB,
			Healthy:      device.Healthy,
		})
	}

	return result
}

func kubernetesAllocationAnnotations(allocations []v1.StaticNodeAllocationStatus) []kubernetesAllocationAnnotation {
	result := []kubernetesAllocationAnnotation{}
	for _, allocation := range allocations {
		for _, device := range allocation.Devices {
			if device.UUID == "" {
				continue
			}

			result = append(result, kubernetesAllocationAnnotation{
				UUID:      device.UUID,
				Product:   device.Product,
				NodeID:    device.NodeID,
				MemoryMiB: device.MemoryMiB,
				CoreUnits: device.CoreUnits,
			})
		}
	}

	return result
}
