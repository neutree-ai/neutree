package kubernetes

import (
	"context"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator/resourceparser"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestAnnotationWriterPatchesOnlyLocalNodeAndPods(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	localNode := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a"}}
	remoteNode := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-b"}}
	localPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "replica-a", Namespace: "default"},
		Spec:       corev1.PodSpec{NodeName: "node-a"},
	}
	sameNamePod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "replica-a", Namespace: "other"},
		Spec:       corev1.PodSpec{NodeName: "node-a"},
	}
	staleLocalPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "stale-replica",
			Namespace: "default",
			Annotations: map[string]string{
				resourceparser.NeutreeAcceleratorAllocationsAnnotation: `[{"uuid":"GPU-stale"}]`,
			},
		},
		Spec: corev1.PodSpec{NodeName: "node-a"},
	}
	remotePod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "replica-b", Namespace: "default"},
		Spec:       corev1.PodSpec{NodeName: "node-b"},
	}

	ctrClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithIndex(&corev1.Pod{}, "spec.nodeName", podNodeNameIndex).
		WithObjects(localNode, remoteNode, localPod, sameNamePod, staleLocalPod, remotePod).
		Build()
	writer := &AnnotationWriter{
		Client:   ctrClient,
		NodeName: "node-a",
	}
	snapshot := &v1.NodeDeviceSnapshot{
		Accelerator: v1.StaticNodeAcceleratorStatus{
			Type: v1.AcceleratorTypeNVIDIAGPU.String(),
			Devices: []v1.StaticNodeAcceleratorDeviceStatus{
				{ID: "0", UUID: "GPU-abc", ProductModel: "NVIDIA_A100", MinorNumber: intPtr(0), MemoryMiB: 81920, Healthy: true},
				{ID: "1", UUID: "GPU-unknown", ProductModel: "NVIDIA_A100", MemoryMiB: 81920, Healthy: true},
			},
		},
		Allocations: []v1.StaticNodeAllocationStatus{
			{
				WorkloadType: "endpoint",
				Endpoint:     "chat",
				ReplicaID:    "default/replica-a",
				Devices: []v1.DeviceAllocation{
					{
						UUID:          "GPU-abc",
						Product:       "NVIDIA_A100",
						VDeviceIndex:  "1",
						MemoryMiB:     8192,
						UsedMemoryMiB: 4096,
						CoreUnits:     50,
					},
				},
			},
		},
	}

	require.NoError(t, writer.Write(context.Background(), snapshot))

	var patchedNode corev1.Node
	require.NoError(t, ctrClient.Get(context.Background(), client.ObjectKey{Name: "node-a"}, &patchedNode))
	devicesAnnotation := patchedNode.Annotations[resourceparser.NeutreeAcceleratorDevicesAnnotation]
	assert.Contains(t, devicesAnnotation, "GPU-abc")
	assert.Contains(t, devicesAnnotation, `"minor_number":0`)
	assert.Contains(t, devicesAnnotation, "GPU-unknown")
	assert.NotContains(t, devicesAnnotation, `"minor_number":-1`)

	var untouchedNode corev1.Node
	require.NoError(t, ctrClient.Get(context.Background(), client.ObjectKey{Name: "node-b"}, &untouchedNode))
	assert.Empty(t, untouchedNode.Annotations)

	var patchedPod corev1.Pod
	require.NoError(t, ctrClient.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "replica-a"}, &patchedPod))
	assert.Contains(t, patchedPod.Annotations[resourceparser.NeutreeAcceleratorAllocationsAnnotation], "GPU-abc")
	assert.Contains(t, patchedPod.Annotations[resourceparser.NeutreeAcceleratorAllocationsAnnotation], `"vdevice_index":"1"`)
	assert.Contains(t, patchedPod.Annotations[resourceparser.NeutreeAcceleratorAllocationsAnnotation], `"memory_mib":8192`)
	assert.Contains(t, patchedPod.Annotations[resourceparser.NeutreeAcceleratorAllocationsAnnotation], `"used_memory_mib":4096`)
	assert.Contains(t, patchedPod.Annotations[resourceparser.NeutreeAcceleratorAllocationsAnnotation], `"core_units":50`)

	var sameNameOtherNamespacePod corev1.Pod
	require.NoError(t, ctrClient.Get(context.Background(), client.ObjectKey{Namespace: "other", Name: "replica-a"}, &sameNameOtherNamespacePod))
	assert.Empty(t, sameNameOtherNamespacePod.Annotations)

	var untouchedPod corev1.Pod
	require.NoError(t, ctrClient.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "replica-b"}, &untouchedPod))
	assert.Empty(t, untouchedPod.Annotations)

	var stalePod corev1.Pod
	require.NoError(t, ctrClient.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "stale-replica"}, &stalePod))
	assert.NotContains(t, stalePod.Annotations, resourceparser.NeutreeAcceleratorAllocationsAnnotation)
}

func podNodeNameIndex(object client.Object) []string {
	pod, ok := object.(*corev1.Pod)
	if !ok || pod.Spec.NodeName == "" {
		return nil
	}

	return []string{pod.Spec.NodeName}
}

func intPtr(value int) *int {
	return &value
}
