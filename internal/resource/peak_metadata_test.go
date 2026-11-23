package resource

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	k8sresource "k8s.io/apimachinery/pkg/api/resource"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
	"github.com/neutree-ai/neutree/internal/accelerator/resourceparser"
)

func TestControlPlanePeakWithoutNewNodeAgentFields(t *testing.T) {
	const product = "NVIDIA-L20"
	quantities := map[corev1.ResourceName]k8sresource.Quantity{plugin.NvidiaGPUKubernetesResource: k8sresource.MustParse("1")}
	for _, tc := range []struct{ name, annotation, memory string }{
		{"no annotation", "", "15360"},
		{"old annotation", `[{"uuid":"gpu","memory_mib":15360,"healthy":true}]`, "15360"},
		{"ignore stale node peak", `[{"uuid":"gpu","memory_mib":15360,"healthy":true,"peak_fp16_tflops":999}]`, "15360"},
		{"no agent or memory label", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, devices, metadata, err := transformKubernetesNodeResources(resourceparser.KubernetesNodeResourceContext{
				AllocatableResources: quantities, AvailableResources: quantities,
				Labels:      map[string]string{plugin.NvidiaGPUKubernetesNodeSelectorKey: product, plugin.NvidiaGPUMemoryNodeLabelKey: tc.memory},
				Annotations: map[string]string{resourceparser.NeutreeAcceleratorDevicesAnnotation: tc.annotation},
			}, map[string]resourceparser.ResourceParser{"nvidia_gpu": &plugin.GPUResourceParser{}})
			require.NoError(t, err)
			result := buildClusterResourcesFromResourceNodes([]ResourceNode{{ID: "node", Status: &v1.NodeResourceStatus{ResourceStatus: *status, Devices: devices}, AcceleratorMetadata: metadata}})
			spec := result.AcceleratorMetadata[v1.AcceleratorTypeNVIDIAGPU].Products[product]
			require.NotNil(t, spec.PeakFP16TFLOPS)
			require.Equal(t, 119.5, *spec.PeakFP16TFLOPS)
			require.Equal(t, float64(1), result.Allocatable.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU].Quantity)
			if tc.memory != "" {
				require.Equal(t, float64(15360), spec.MemoryTotalMiB)
			}
		})
	}
	parser := &plugin.GPUResourceParser{}
	ray, err := parser.ParseFromRay(map[string]float64{"GPU": 1, product: 1})
	require.NoError(t, err)
	for _, raw := range []string{
		`{"accelerator":{"type":"nvidia_gpu","devices":[{"uuid":"gpu","memory_mib":15360,"healthy":true}]}}`,
		`{"accelerator":{"type":"nvidia_gpu"}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			// Fixtures deliberately use the old node-agent payload without peak fields.
			var snapshot v1.NodeDeviceSnapshot
			require.NoError(t, json.Unmarshal([]byte(raw), &snapshot))
			base := staticNodeBaseResourceNodeForTest("node", 4, 8, 4, 8, product)
			base.AcceleratorMetadata = ray.AcceleratorMetadata
			node := &v1.StaticNode{Metadata: &v1.Metadata{Name: "node"}, Spec: &v1.StaticNodeSpec{IP: "node"}, Status: &v1.StaticNodeStatus{Accelerator: &snapshot.Accelerator}}
			resourceNode, ok := resourceNodeFromStaticNodeDeviceSnapshot(node, &base)
			require.True(t, ok)
			result := buildClusterResourcesFromResourceNodes([]ResourceNode{resourceNode})
			peak := result.AcceleratorMetadata[v1.AcceleratorTypeNVIDIAGPU].Products[product].PeakFP16TFLOPS
			require.NotNil(t, peak)
			require.Equal(t, 119.5, *peak)
			require.Equal(t, float64(1), result.Allocatable.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU].Quantity)
		})
	}
}

func TestPeakAggregationFillsMissingWithoutErasingKnownMetadata(t *testing.T) {
	peak := 65.0
	for _, values := range [][]*float64{{nil, &peak, nil}, {&peak, nil, &peak}} {
		target := map[v1.AcceleratorType]*v1.AcceleratorMetadata{}
		for _, value := range values {
			mergeAcceleratorMetadata(target, map[v1.AcceleratorType]*v1.AcceleratorMetadata{"nvidia_gpu": {Products: map[v1.AcceleratorProduct]*v1.AcceleratorProductMetadata{"T4": {MemoryTotalMiB: 15360, PeakFP16TFLOPS: value}}}})
		}
		product := target["nvidia_gpu"].Products["T4"]
		require.Equal(t, peak, *product.PeakFP16TFLOPS)
		require.Equal(t, float64(15360), product.MemoryTotalMiB)
	}
	unknown := &v1.AcceleratorProductMetadata{}
	unknown.Merge(&v1.AcceleratorProductMetadata{})
	require.Nil(t, unknown.PeakFP16TFLOPS)
}

func TestUnknownProductPeakRemainsUnknown(t *testing.T) {
	parser := &plugin.GPUResourceParser{}
	info, err := parser.ParseFromKubernetes(map[corev1.ResourceName]k8sresource.Quantity{plugin.NvidiaGPUKubernetesResource: k8sresource.MustParse("1")}, map[string]string{plugin.NvidiaGPUKubernetesNodeSelectorKey: "NVIDIA_unknown", plugin.NvidiaGPUMemoryNodeLabelKey: "15360"})
	require.NoError(t, err)
	require.Nil(t, info.AcceleratorMetadata[v1.AcceleratorTypeNVIDIAGPU].Products["NVIDIA_unknown"].PeakFP16TFLOPS)
	info, err = parser.ParseFromRay(map[string]float64{"GPU": 1, "NVIDIA_unknown": 1})
	require.NoError(t, err)
	require.Nil(t, info.AcceleratorMetadata)
}
