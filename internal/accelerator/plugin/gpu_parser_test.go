package plugin

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestNVIDIAGPU_ParseFromKubernetes(t *testing.T) {

	tests := []struct {
		name               string
		kubernetesResource map[corev1.ResourceName]resource.Quantity
		nodeLabels         map[string]string
		expected           *v1.ResourceInfo
		wantErr            bool
	}{
		{
			name: "Node with NVIDIA GPU and product label",
			kubernetesResource: map[corev1.ResourceName]resource.Quantity{
				corev1.ResourceName(NvidiaGPUKubernetesResource): resource.MustParse("4"),
			},
			nodeLabels: map[string]string{
				NvidiaGPUKubernetesNodeSelectorKey: "NVIDIA_L20",
			},
			expected: &v1.ResourceInfo{
				AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
					v1.AcceleratorTypeNVIDIAGPU: {
						Quantity: 4,
						ProductGroups: map[v1.AcceleratorProduct]float64{
							"NVIDIA_L20": 4,
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "Node with zero NVIDIA GPU",
			kubernetesResource: map[corev1.ResourceName]resource.Quantity{
				corev1.ResourceName(NvidiaGPUKubernetesResource): resource.MustParse("0"),
			},
			nodeLabels: map[string]string{
				NvidiaGPUKubernetesNodeSelectorKey: "NVIDIA_L20",
			},
			expected: &v1.ResourceInfo{
				AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
					v1.AcceleratorTypeNVIDIAGPU: {
						Quantity: 0,
						ProductGroups: map[v1.AcceleratorProduct]float64{
							"NVIDIA_L20": 0,
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "Node with NVIDIA GPU but no product label",
			kubernetesResource: map[corev1.ResourceName]resource.Quantity{
				corev1.ResourceName(NvidiaGPUKubernetesResource): resource.MustParse("2"),
			},
			nodeLabels: map[string]string{},
			expected: &v1.ResourceInfo{
				AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
					v1.AcceleratorTypeNVIDIAGPU: {
						Quantity: 2,
					},
				},
			},
			wantErr: false,
		},
		{
			name: "Node without NVIDIA GPU",
			kubernetesResource: map[corev1.ResourceName]resource.Quantity{
				corev1.ResourceCPU: resource.MustParse("16"),
			},
			nodeLabels: map[string]string{},
			expected:   nil,
			wantErr:    false,
		},
		{
			name:               "Nil resource map",
			kubernetesResource: nil,
			nodeLabels:         map[string]string{},
			expected:           nil,
			wantErr:            true,
		},
		{
			name: "Nil node labels map",
			kubernetesResource: map[corev1.ResourceName]resource.Quantity{
				corev1.ResourceName(NvidiaGPUKubernetesResource): resource.MustParse("2"),
			},
			nodeLabels: nil,
			expected:   nil,
			wantErr:    true,
		},
	}

	parser := &GPUResourceParser{}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parser.ParseFromKubernetes(tt.kubernetesResource, tt.nodeLabels)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseFromKubernetes() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			equal, _, err := util.JsonEqual(result, tt.expected)
			if err != nil {
				t.Errorf("Error comparing results: %v", err)
			}

			if !equal {
				t.Errorf("ParseFromKubernetes() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

func TestNVIDIAGPU_ParseFromKubernetesHAMiVirtualization(t *testing.T) {
	parser := &GPUResourceParser{}
	result, err := parser.ParseFromKubernetes(map[corev1.ResourceName]resource.Quantity{
		NvidiaGPUKubernetesResource: resource.MustParse("20"),
		NvidiaGPUMemoryResource:     resource.MustParse("40960"),
		NvidiaGPUCoreResource:       resource.MustParse("200"),
	}, map[string]string{
		NvidiaGPUKubernetesNodeSelectorKey: "Tesla-T4",
		NvidiaGPUMemoryNodeLabelKey:        "15360",
		NvidiaGPUVirtualizationLabelKey:    "true",
		NvidiaGPUCountResource:             "2",
	})
	if err != nil {
		t.Fatalf("ParseFromKubernetes() error = %v", err)
	}

	expected := &v1.ResourceInfo{
		AcceleratorMetadata: map[v1.AcceleratorType]*v1.AcceleratorMetadata{
			v1.AcceleratorTypeNVIDIAGPU: {
				Products: map[v1.AcceleratorProduct]*v1.AcceleratorProductMetadata{
					"Tesla-T4": {
						MemoryTotalMiB: 15360,
					},
				},
			},
		},
		AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
			v1.AcceleratorTypeNVIDIAGPU: {
				Quantity: 2,
				ProductGroups: map[v1.AcceleratorProduct]float64{
					"Tesla-T4": 2,
				},
				Products: map[v1.AcceleratorProduct]*v1.AcceleratorProductResource{
					"Tesla-T4": {
						Quantity: 2,
						Virtualization: &v1.AcceleratorVirtualizationResource{
							MemoryMiB: 40960,
							CoreUnits: 200,
						},
					},
				},
			},
		},
	}

	equal, diff, err := util.JsonEqual(result, expected)
	if err != nil {
		t.Fatalf("JsonEqual() error = %v", err)
	}
	if !equal {
		t.Fatalf("ParseFromKubernetes() differs from expected:\n%s", diff)
	}
}

func TestNVIDIAGPU_ParseFromKubernetesDoesNotInferVirtualizationFromHAMiResources(t *testing.T) {
	parser := &GPUResourceParser{}
	result, err := parser.ParseFromKubernetes(map[corev1.ResourceName]resource.Quantity{
		NvidiaGPUKubernetesResource: resource.MustParse("20"),
		NvidiaGPUMemoryResource:     resource.MustParse("40960"),
		NvidiaGPUCoreResource:       resource.MustParse("200"),
	}, map[string]string{
		NvidiaGPUKubernetesNodeSelectorKey: "Tesla-T4",
		NvidiaGPUMemoryNodeLabelKey:        "15360",
		NvidiaGPUCountResource:             "2",
	})
	if err != nil {
		t.Fatalf("ParseFromKubernetes() error = %v", err)
	}

	expected := &v1.ResourceInfo{
		AcceleratorMetadata: map[v1.AcceleratorType]*v1.AcceleratorMetadata{
			v1.AcceleratorTypeNVIDIAGPU: {
				Products: map[v1.AcceleratorProduct]*v1.AcceleratorProductMetadata{
					"Tesla-T4": {
						MemoryTotalMiB: 15360,
					},
				},
			},
		},
		AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
			v1.AcceleratorTypeNVIDIAGPU: {
				Quantity: 20,
				ProductGroups: map[v1.AcceleratorProduct]float64{
					"Tesla-T4": 20,
				},
				Products: map[v1.AcceleratorProduct]*v1.AcceleratorProductResource{
					"Tesla-T4": {
						Quantity: 20,
					},
				},
			},
		},
	}

	equal, diff, err := util.JsonEqual(result, expected)
	if err != nil {
		t.Fatalf("JsonEqual() error = %v", err)
	}
	if !equal {
		t.Fatalf("ParseFromKubernetes() differs from expected:\n%s", diff)
	}
}

func TestNVIDIAGPU_ParseFromKubernetesVirtualizationRequiresGPUCountLabel(t *testing.T) {
	parser := &GPUResourceParser{}
	result, err := parser.ParseFromKubernetes(map[corev1.ResourceName]resource.Quantity{
		NvidiaGPUKubernetesResource: resource.MustParse("20"),
		NvidiaGPUMemoryResource:     resource.MustParse("40960"),
		NvidiaGPUCoreResource:       resource.MustParse("200"),
	}, map[string]string{
		NvidiaGPUKubernetesNodeSelectorKey: "Tesla-T4",
		NvidiaGPUVirtualizationLabelKey:    "true",
	})
	if err != nil {
		t.Fatalf("ParseFromKubernetes() error = %v", err)
	}

	group := result.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU]
	if group.Quantity != 0 {
		t.Fatalf("expected missing %s label to report 0 physical GPUs, got %v",
			NvidiaGPUCountResource, group.Quantity)
	}
}

func TestGPUParser_ParserFromRay(t *testing.T) {
	parser := &GPUResourceParser{}

	tests := []struct {
		name        string
		rayResource map[string]float64
		expected    *v1.ResourceInfo
		wantErr     bool
	}{
		{
			name: "Ray resource with NVIDIA GPU",
			rayResource: map[string]float64{
				"GPU":                     2,
				"NVIDIA_L20":              2,
				"custom_resource_example": 5,
			},
			expected: &v1.ResourceInfo{
				AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
					v1.AcceleratorTypeNVIDIAGPU: {
						Quantity: 2,
						ProductGroups: map[v1.AcceleratorProduct]float64{
							"NVIDIA_L20": 2,
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "Ray resource with zero NVIDIA GPU",
			rayResource: map[string]float64{
				"GPU":                     0,
				"NVIDIA_L20":              0,
				"custom_resource_example": 5,
			},
			expected: &v1.ResourceInfo{
				AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
					v1.AcceleratorTypeNVIDIAGPU: {
						Quantity: 0,
						ProductGroups: map[v1.AcceleratorProduct]float64{
							"NVIDIA_L20": 0,
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "Ray resource with AMD GPU",
			rayResource: map[string]float64{
				"CPU":                     16,
				"GPU":                     4,
				"AMD_Instinct_MI300X_OAM": 4,
			},
			expected: nil,
			wantErr:  false,
		},
		{
			name: "Ray resource without NVIDIA GPU",
			rayResource: map[string]float64{
				"CPU": 16,
			},
			expected: nil,
			wantErr:  false,
		},
		{
			name:        "Nil Ray resource map",
			rayResource: nil,
			expected:    nil,
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parser.ParseFromRay(tt.rayResource)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseFromRay() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			equal, diff, err := util.JsonEqual(result, tt.expected)
			if err != nil {
				t.Errorf("JsonEqual() error = %v", err)
				return
			}
			if !equal {
				t.Errorf("ParseFromRay() result differs from expected:\n%s", diff)
			}
		})
	}

}
