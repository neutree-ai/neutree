package resource

import (
	"context"
	"encoding/json"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator/resourceparser"
	metricskubernetes "github.com/neutree-ai/neutree/internal/observability/neutreemetrics/kubernetes"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestDevicePeakSurvivesKubernetesAndStaticAggregation(t *testing.T) {
	peak := 65.0
	snapshot := &v1.NodeDeviceSnapshot{Accelerator: v1.StaticNodeAcceleratorStatus{
		Type: "nvidia_gpu", Devices: []v1.StaticNodeAcceleratorDeviceStatus{{
			UUID: "gpu", ProductName: "Tesla T4", ProductModel: "Tesla-T4", MemoryMiB: 15360, Healthy: true, PeakFP16TFLOPS: &peak,
		}},
	}}
	// Exercise the SSH JSON transport rather than only copying Go structs.
	payload, err := json.Marshal(snapshot)
	require.NoError(t, err)
	var received v1.NodeDeviceSnapshot
	require.NoError(t, json.Unmarshal(payload, &received))
	node := &v1.StaticNode{Metadata: &v1.Metadata{Name: "node"}, Spec: &v1.StaticNodeSpec{IP: "node"}, Status: &v1.StaticNodeStatus{Accelerator: &received.Accelerator}}
	base := staticNodeBaseResourceNodeForTest("node", 4, 8, 4, 8, "Tesla-T4")
	sshNode, ok := resourceNodeFromStaticNodeDeviceSnapshot(node, &base)
	require.True(t, ok)
	ssh := buildClusterResourcesFromResourceNodes([]ResourceNode{sshNode})
	require.Equal(t, peak, *ssh.AcceleratorMetadata[v1.AcceleratorTypeNVIDIAGPU].Products["Tesla-T4"].PeakFP16TFLOPS)

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	kube := fake.NewClientBuilder().WithScheme(scheme).WithIndex(&corev1.Pod{}, "spec.nodeName", func(object client.Object) []string { return []string{object.(*corev1.Pod).Spec.NodeName} }).WithObjects(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node"}}).Build()
	writer := &metricskubernetes.AnnotationWriter{Client: kube, NodeName: "node"}
	require.NoError(t, writer.Write(context.Background(), snapshot))
	var kubeNode corev1.Node
	require.NoError(t, kube.Get(context.Background(), client.ObjectKey{Name: "node"}, &kubeNode))
	require.Contains(t, kubeNode.Annotations[resourceparser.NeutreeAcceleratorDevicesAnnotation], "peak_fp16_tflops")
	require.NotEmpty(t, kubeNode.Annotations)
	devices, err := parseKubernetesNeutreeDeviceResources(kubeNode.Annotations)
	require.NoError(t, err)
	require.Len(t, devices, 1)
	parsed := buildKubernetesNeutreeAcceleratorResources(devices, nil, v1.AcceleratorTypeNVIDIAGPU, "Tesla-T4")
	k8s := buildClusterResourcesFromResourceNodes([]ResourceNode{{ID: "node", Status: &v1.NodeResourceStatus{ResourceStatus: v1.ResourceStatus{Allocatable: parsed.Allocatable, Available: parsed.Available}, Devices: parsed.Devices}, AcceleratorMetadata: parsed.AcceleratorMetadata}})
	require.Equal(t, ssh.AcceleratorMetadata, k8s.AcceleratorMetadata)
	require.Equal(t, int64(15360), k8s.NodeResources["node"].Devices[0].Allocatable.MemoryMiB)
	require.Equal(t, k8s.NodeResources["node"].Devices[0].Allocatable, k8s.NodeResources["node"].Devices[0].Available)
}

func TestPeakAggregationFillsMissingAndKeepsConflictsUnknown(t *testing.T) {
	first, second := 65.0, 70.0
	for _, values := range [][]*float64{{nil, &first, &second, &first}, {&second, &first, nil, &second}, {&first, nil, &first, &second}} {
		target := map[v1.AcceleratorType]*v1.AcceleratorMetadata{}
		for _, value := range values {
			mergeAcceleratorMetadata(target, map[v1.AcceleratorType]*v1.AcceleratorMetadata{"nvidia_gpu": {Products: map[v1.AcceleratorProduct]*v1.AcceleratorProductMetadata{"T4": {MemoryTotalMiB: 15360, PeakFP16TFLOPS: value}}}})
		}
		require.Nil(t, target["nvidia_gpu"].Products["T4"].PeakFP16TFLOPS)
	}
	product := &v1.AcceleratorProductMetadata{MemoryTotalMiB: 15360}
	product.Merge(&v1.AcceleratorProductMetadata{PeakFP16TFLOPS: &first})
	require.Equal(t, first, *product.PeakFP16TFLOPS)
	require.Equal(t, float64(15360), product.MemoryTotalMiB)
	product.Merge(&v1.AcceleratorProductMetadata{})
	require.Equal(t, first, *product.PeakFP16TFLOPS)
}
