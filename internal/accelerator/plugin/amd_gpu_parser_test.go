package plugin

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestAMDGPU_ParseFromKubernetes(t *testing.T) {

	tests := []struct {
		name               string
		kubernetesResource map[corev1.ResourceName]resource.Quantity
		nodeLabels         map[string]string
		expected           *v1.ResourceInfo
		wantErr            bool
	}{
		{
			name: "Node with AMD GPU and product label",
			kubernetesResource: map[corev1.ResourceName]resource.Quantity{
				corev1.ResourceName(AMDGPUKubernetesResource): resource.MustParse("4"),
			},
			nodeLabels: map[string]string{
				AMDGPUKubernetesNodeSelectorKey: "AMD_Instinct_MI300X_OAM",
			},
			expected: &v1.ResourceInfo{
				AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
					v1.AcceleratorTypeAMDGPU: {
						Quantity: 4,
						ProductGroups: map[v1.AcceleratorProduct]float64{
							"AMD_Instinct_MI300X_OAM": 4,
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "Node with AMD GPU but no product label",
			kubernetesResource: map[corev1.ResourceName]resource.Quantity{
				corev1.ResourceName(AMDGPUKubernetesResource): resource.MustParse("2"),
			},
			nodeLabels: map[string]string{},
			expected: &v1.ResourceInfo{
				AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
					v1.AcceleratorTypeAMDGPU: {
						Quantity: 2,
					},
				},
			},
			wantErr: false,
		},
		{
			name: "Node with zero AMD GPU",
			kubernetesResource: map[corev1.ResourceName]resource.Quantity{
				corev1.ResourceName(AMDGPUKubernetesResource): resource.MustParse("0"),
			},
			nodeLabels: map[string]string{
				AMDGPUKubernetesNodeSelectorKey: "AMD_Instinct_MI300X_OAM",
			},
			expected: &v1.ResourceInfo{
				AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
					v1.AcceleratorTypeAMDGPU: {
						Quantity: 0,
						ProductGroups: map[v1.AcceleratorProduct]float64{
							"AMD_Instinct_MI300X_OAM": 0,
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "Node without AMD GPU",
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
				corev1.ResourceName(AMDGPUKubernetesResource): resource.MustParse("2"),
			},
			nodeLabels: nil,
			expected:   nil,
			wantErr:    true,
		},
	}

	parser := &AMDGPUResourceParser{}

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

func TestAMDGPU_ParseFromRay(t *testing.T) {
	tests := []struct {
		name        string
		rayResource map[string]float64
		expected    *v1.ResourceInfo
		wantErr     bool
	}{
		{
			name: "Ray resource with AMD GPU",
			rayResource: map[string]float64{
				"GPU":                     2,
				"AMD_Instinct_MI300X_OAM": 2,
				"custom_resource_example": 5,
			},
			expected: &v1.ResourceInfo{
				AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
					v1.AcceleratorTypeAMDGPU: {
						Quantity: 2,
						ProductGroups: map[v1.AcceleratorProduct]float64{
							"AMD_Instinct_MI300X_OAM": 2,
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "Ray resource with zero AMD GPU",
			rayResource: map[string]float64{
				"GPU":                     0,
				"AMD_Instinct_MI300X_OAM": 0,
				"custom_resource_example": 5,
			},
			expected: &v1.ResourceInfo{
				AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
					v1.AcceleratorTypeAMDGPU: {
						Quantity: 0,
						ProductGroups: map[v1.AcceleratorProduct]float64{
							"AMD_Instinct_MI300X_OAM": 0,
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "Ray resource with NVIDIA GPU",
			rayResource: map[string]float64{
				"CPU":        16,
				"GPU":        4,
				"NVIDIA_L20": 4,
			},
			expected: nil,
			wantErr:  false,
		},
		{
			name: "Ray resource without AMD GPU",
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

	parser := &AMDGPUResourceParser{}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parser.ParseFromRay(tt.rayResource)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseFromRay() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			equal, _, err := util.JsonEqual(result, tt.expected)
			if err != nil {
				t.Errorf("Error comparing results: %v", err)
			}

			if !equal {
				t.Errorf("ParseFromRay() = %v, expected %v", result, tt.expected)
			}
		})
	}
}
