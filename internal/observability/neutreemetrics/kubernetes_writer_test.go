package neutreemetrics

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

func TestKubernetesAnnotationWriterPatchesOnlyLocalNodeAndPods(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	localNode := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a"}}
	remoteNode := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-b"}}
	localPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "replica-a", Namespace: "default"},
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
		WithObjects(localNode, remoteNode, localPod, staleLocalPod, remotePod).
		Build()
	writer := &KubernetesAnnotationWriter{
		Client:   ctrClient,
		NodeName: "node-a",
	}
	snapshot := &NodeSnapshot{
		Accelerator: v1.StaticNodeAcceleratorStatus{
			Type:         v1.AcceleratorTypeNVIDIAGPU.String(),
			Vendor:       "nvidia",
			ProductModel: "nvidia_gpu",
			Devices: []v1.StaticNodeAcceleratorDeviceStatus{
				{ID: "0", UUID: "GPU-abc", ProductModel: "NVIDIA_A100", MemoryMiB: 81920, Healthy: true},
			},
		},
		Allocations: []v1.StaticNodeAllocationStatus{
			{
				WorkloadType: "endpoint",
				Endpoint:     "chat",
				ReplicaID:    "replica-a",
				Devices: []v1.DeviceAllocation{
					{UUID: "GPU-abc", Product: "NVIDIA_A100", MemoryMiB: 81920},
				},
			},
		},
	}

	require.NoError(t, writer.Write(context.Background(), snapshot))

	var patchedNode corev1.Node
	require.NoError(t, ctrClient.Get(context.Background(), client.ObjectKey{Name: "node-a"}, &patchedNode))
	assert.Contains(t, patchedNode.Annotations[resourceparser.NeutreeAcceleratorDevicesAnnotation], "GPU-abc")

	var untouchedNode corev1.Node
	require.NoError(t, ctrClient.Get(context.Background(), client.ObjectKey{Name: "node-b"}, &untouchedNode))
	assert.Empty(t, untouchedNode.Annotations)

	var patchedPod corev1.Pod
	require.NoError(t, ctrClient.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "replica-a"}, &patchedPod))
	assert.Contains(t, patchedPod.Annotations[resourceparser.NeutreeAcceleratorAllocationsAnnotation], "GPU-abc")

	var untouchedPod corev1.Pod
	require.NoError(t, ctrClient.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "replica-b"}, &untouchedPod))
	assert.Empty(t, untouchedPod.Annotations)

	var stalePod corev1.Pod
	require.NoError(t, ctrClient.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "stale-replica"}, &stalePod))
	assert.NotContains(t, stalePod.Annotations, resourceparser.NeutreeAcceleratorAllocationsAnnotation)
}
