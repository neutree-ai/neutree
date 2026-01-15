package plugin

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
	"k8s.io/utils/pointer"
)

func TestAMDGPU_ConvertToKubernetes(t *testing.T) {
	tests := []struct {
		name             string
		resourceInfo     *v1.ResourceSpec
		expectedResource *v1.KubernetesResourceSpec
		wantErr          bool
	}{
		{
			name: "Convert AMD GPU resource spec to Kubernetes",
			resourceInfo: &v1.ResourceSpec{
				GPU:    pointer.String("3"),
				CPU:    pointer.String("16"),
				Memory: pointer.String("64"),
				Accelerator: map[string]string{
					v1.AcceleratorTypeKey:    string(v1.AcceleratorTypeAMDGPU),
					v1.AcceleratorProductKey: "AMD_Instinct_MI300X_OAM",
				},
			},
			expectedResource: &v1.KubernetesResourceSpec{
				Requests: map[string]string{
					AMDGPUKubernetesResource.String(): "3",
				},
				Limits: map[string]string{
					AMDGPUKubernetesResource.String(): "3",
				},
				NodeSelector: map[string]string{
					AMDGPUKubernetesNodeSelectorKey: "AMD_Instinct_MI300X_OAM",
				},
			},
			wantErr: false,
		},
		{
			name: "Convert AMD GPU resource spec with zero GPU to Kubernetes",
			resourceInfo: &v1.ResourceSpec{
				GPU:    pointer.String("0"),
				CPU:    pointer.String("16"),
				Memory: pointer.String("64"),
				Accelerator: map[string]string{
					v1.AcceleratorTypeKey:    string(v1.AcceleratorTypeAMDGPU),
					v1.AcceleratorProductKey: "AMD_Instinct_MI300X_OAM",
				},
			},
			expectedResource: nil,
			wantErr:          false,
		},
		{
			name: "Convert AMD GPU resource spec with none product to Kubernetes",
			resourceInfo: &v1.ResourceSpec{
				GPU:    pointer.String("3"),
				CPU:    pointer.String("16"),
				Memory: pointer.String("64"),
				Accelerator: map[string]string{
					v1.AcceleratorTypeKey: string(v1.AcceleratorTypeAMDGPU),
				},
			},
			expectedResource: &v1.KubernetesResourceSpec{
				Requests: map[string]string{
					AMDGPUKubernetesResource.String(): "3",
				},
				Limits: map[string]string{
					AMDGPUKubernetesResource.String(): "3",
				},
				NodeSelector: map[string]string{},
			},
			wantErr: false,
		},
		{
			name: "Convert AMD GPU resource spec with nil GPU to Kubernetes",
			resourceInfo: &v1.ResourceSpec{
				CPU:    pointer.String("16"),
				Memory: pointer.String("64"),
				Accelerator: map[string]string{
					v1.AcceleratorTypeKey:    string(v1.AcceleratorTypeAMDGPU),
					v1.AcceleratorProductKey: "AMD_Instinct_MI300X_OAM",
				},
			},
			expectedResource: nil,
			wantErr:          false,
		},
		{
			name:             "Convert nil resource spec",
			resourceInfo:     nil,
			expectedResource: nil,
			wantErr:          true,
		},
		{
			name: "Convert AMD GPU resource spec with wrong accelerator type",
			resourceInfo: &v1.ResourceSpec{
				GPU:    pointer.String("2"),
				CPU:    pointer.String("8"),
				Memory: pointer.String("32"),
				Accelerator: map[string]string{
					v1.AcceleratorTypeKey:    string(v1.AcceleratorTypeNVIDIAGPU),
					v1.AcceleratorProductKey: "NVIDIA_A100",
				},
			},
			expectedResource: nil,
			wantErr:          false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			converter := NewAMDGPUConverter()
			k8sResource, err := converter.ConvertToKubernetes(tt.resourceInfo)
			if (err != nil) != tt.wantErr {
				t.Errorf("ConvertToKubernetes() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			equal, _, err := util.JsonEqual(k8sResource, tt.expectedResource)
			if err != nil {
				t.Errorf("JsonEqual() error = %v", err)
				return
			}
			if !equal {
				t.Errorf("ConvertToKubernetes() = %v, expected %v", k8sResource, tt.expectedResource)
			}
		})
	}
}

func TestAMDGPU_ConvertToRay(t *testing.T) {
	tests := []struct {
		name         string
		resourceInfo *v1.ResourceSpec
		expectedRay  *v1.RayResourceSpec
		wantErr      bool
	}{
		{
			name: "Convert AMD GPU resource spec to Ray",
			resourceInfo: &v1.ResourceSpec{
				GPU:    pointer.String("2"),
				CPU:    pointer.String("8"),
				Memory: pointer.String("32"),
				Accelerator: map[string]string{
					v1.AcceleratorTypeKey:    string(v1.AcceleratorTypeAMDGPU),
					v1.AcceleratorProductKey: "AMD_Instinct_MI300X_VF",
				},
			},
			expectedRay: &v1.RayResourceSpec{
				NumGPUs: 2,
				Resources: map[string]float64{
					"AMD_Instinct_MI300X_VF": 2,
				},
			},
			wantErr: false,
		},
		{
			name: "Convert AMD GPU resource spec with zero GPU to Ray",
			resourceInfo: &v1.ResourceSpec{
				GPU:    pointer.String("0"),
				CPU:    pointer.String("8"),
				Memory: pointer.String("32"),
				Accelerator: map[string]string{
					v1.AcceleratorTypeKey:    string(v1.AcceleratorTypeAMDGPU),
					v1.AcceleratorProductKey: "AMD_Instinct_MI300X_VF",
				},
			},
			expectedRay: nil,
			wantErr:     false,
		},
		{
			name: "Convert AMD GPU resource spec with none product to Ray",
			resourceInfo: &v1.ResourceSpec{
				GPU:    pointer.String("2"),
				CPU:    pointer.String("8"),
				Memory: pointer.String("32"),
				Accelerator: map[string]string{
					v1.AcceleratorTypeKey: string(v1.AcceleratorTypeAMDGPU),
				},
			},
			expectedRay: &v1.RayResourceSpec{
				NumGPUs:   2,
				Resources: map[string]float64{},
			},
			wantErr: false,
		},
		{
			name: "Convert AMD GPU resource spec with nil GPU to Ray",
			resourceInfo: &v1.ResourceSpec{
				CPU:    pointer.String("8"),
				Memory: pointer.String("32"),
				Accelerator: map[string]string{
					v1.AcceleratorTypeKey:    string(v1.AcceleratorTypeAMDGPU),
					v1.AcceleratorProductKey: "AMD_Instinct_MI300X_VF",
				},
			},
			expectedRay: nil,
			wantErr:     false,
		},
		{
			name: "Convert AMD GPU resource spec without GPU",
			resourceInfo: &v1.ResourceSpec{
				CPU:    pointer.String("8"),
				Memory: pointer.String("32"),
			},
			expectedRay: nil,
			wantErr:     false,
		},
		{
			name:         "Convert nil resource spec",
			resourceInfo: nil,
			expectedRay:  nil,
			wantErr:      true,
		},
		{
			name: "Convert AMD GPU resource spec with wrong accelerator type",
			resourceInfo: &v1.ResourceSpec{
				GPU:    pointer.String("2"),
				CPU:    pointer.String("8"),
				Memory: pointer.String("32"),
				Accelerator: map[string]string{
					v1.AcceleratorTypeKey:    string(v1.AcceleratorTypeNVIDIAGPU),
					v1.AcceleratorProductKey: "NVIDIA_A100",
				},
			},
			expectedRay: nil,
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			converter := NewAMDGPUConverter()
			rayResource, err := converter.ConvertToRay(tt.resourceInfo)
			if (err != nil) != tt.wantErr {
				t.Errorf("ConvertToRay() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			equal, _, err := util.JsonEqual(rayResource, tt.expectedRay)
			if err != nil {
				t.Errorf("JsonEqual() error = %v", err)
				return
			}
			if !equal {
				t.Errorf("ConvertToRay() = %v, expected %v", rayResource, tt.expectedRay)
			}
		})
	}
}
