package plugin

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
	"k8s.io/utils/pointer"
)

func TestNVIDIAGPU_ConvertToKubernetes(t *testing.T) {
	tests := []struct {
		name             string
		resourceInfo     *v1.ResourceSpec
		expectedResource *v1.KubernetesResourceSpec
		wantErr          bool
	}{
		{
			name: "Convert NVIDIA GPU resource spec to Kubernetes",
			resourceInfo: &v1.ResourceSpec{
				GPU:    pointer.String("2"),
				CPU:    pointer.String("8"),
				Memory: pointer.String("32"),
				Accelerator: map[string]string{
					v1.AcceleratorTypeKey:    string(v1.AcceleratorTypeNVIDIAGPU),
					v1.AcceleratorProductKey: "Tesla-T4",
				},
			},
			expectedResource: &v1.KubernetesResourceSpec{
				Requests: map[string]string{
					NvidiaGPUKubernetesResource.String(): "2",
				},
				Limits: map[string]string{
					NvidiaGPUKubernetesResource.String(): "2",
				},
				NodeSelector: map[string]string{
					NvidiaGPUKubernetesNodeSelectorKey: "Tesla-T4",
				},
			},
			wantErr: false,
		},
		{
			name: "Convert NVIDIA GPU resource spec with zero GPU to Kubernetes",
			resourceInfo: &v1.ResourceSpec{
				GPU:    pointer.String("0"),
				CPU:    pointer.String("8"),
				Memory: pointer.String("32"),
				Accelerator: map[string]string{
					v1.AcceleratorTypeKey:    string(v1.AcceleratorTypeNVIDIAGPU),
					v1.AcceleratorProductKey: "Tesla-T4",
				},
			},
			expectedResource: &v1.KubernetesResourceSpec{
				Requests:     map[string]string{},
				Limits:       map[string]string{},
				NodeSelector: map[string]string{},
				Env: map[string]string{
					"NVIDIA_VISIBLE_DEVICES": "none",
				},
			},
			wantErr: false,
		},

		{
			name: "Convert NVIDIA GPU resource spec with nil GPU to Kubernetes",
			resourceInfo: &v1.ResourceSpec{
				CPU:    pointer.String("16"),
				Memory: pointer.String("64"),
				Accelerator: map[string]string{
					v1.AcceleratorTypeKey:    string(v1.AcceleratorTypeNVIDIAGPU),
					v1.AcceleratorProductKey: "NVIDIA_A100",
				},
			},
			expectedResource: &v1.KubernetesResourceSpec{
				Requests:     map[string]string{},
				Limits:       map[string]string{},
				NodeSelector: map[string]string{},
				Env: map[string]string{
					"NVIDIA_VISIBLE_DEVICES": "none",
				},
			},
			wantErr: false,
		},
		{
			name: "Convert NVIDIA GPU resource spec with none product to Kubernetes",
			resourceInfo: &v1.ResourceSpec{
				GPU:    pointer.String("3"),
				CPU:    pointer.String("16"),
				Memory: pointer.String("64"),
				Accelerator: map[string]string{
					v1.AcceleratorTypeKey: string(v1.AcceleratorTypeNVIDIAGPU),
				},
			},
			expectedResource: &v1.KubernetesResourceSpec{
				Requests: map[string]string{
					NvidiaGPUKubernetesResource.String(): "3",
				},
				Limits: map[string]string{
					NvidiaGPUKubernetesResource.String(): "3",
				},
				NodeSelector: map[string]string{},
			},
			wantErr: false,
		},
		{
			name:             "Convert nil resource spec",
			resourceInfo:     nil,
			expectedResource: nil,
			wantErr:          true,
		},
		{
			name: "Convert NVIDIA GPU resource spec with wrong accelerator type",
			resourceInfo: &v1.ResourceSpec{
				GPU:    pointer.String("2"),
				CPU:    pointer.String("8"),
				Memory: pointer.String("32"),
				Accelerator: map[string]string{
					v1.AcceleratorTypeKey:    string(v1.AcceleratorTypeAMDGPU),
					v1.AcceleratorProductKey: "AMD_Instinct_MI100",
				},
			},
			expectedResource: nil,
			wantErr:          false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			converter := NewGPUConverter()
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

func TestNVIDIAGPU_ConvertToRay(t *testing.T) {
	tests := []struct {
		name             string
		resourceInfo     *v1.ResourceSpec
		expectedResource *v1.RayResourceSpec
		wantErr          bool
	}{
		{
			name: "Convert NVIDIA GPU resource spec to Ray",
			resourceInfo: &v1.ResourceSpec{
				GPU:    pointer.String("2"),
				CPU:    pointer.String("8"),
				Memory: pointer.String("32"),
				Accelerator: map[string]string{
					v1.AcceleratorTypeKey:    string(v1.AcceleratorTypeNVIDIAGPU),
					v1.AcceleratorProductKey: "NVIDIA-L20",
				},
			},
			expectedResource: &v1.RayResourceSpec{
				NumGPUs: 2,
				Resources: map[string]float64{
					"NVIDIA-L20": 2,
				},
			},
			wantErr: false,
		},
		{
			name: "Convert NVIDIA GPU resource spec with zero GPU to Ray",
			resourceInfo: &v1.ResourceSpec{
				GPU:    pointer.String("0"),
				CPU:    pointer.String("8"),
				Memory: pointer.String("32"),
				Accelerator: map[string]string{
					v1.AcceleratorTypeKey:    string(v1.AcceleratorTypeNVIDIAGPU),
					v1.AcceleratorProductKey: "NVIDIA-L20",
				},
			},
			expectedResource: nil,
			wantErr:          false,
		},
		{
			name: "Convert NVIDIA GPU resource spec with nil GPU to Ray",
			resourceInfo: &v1.ResourceSpec{
				CPU:    pointer.String("16"),
				Memory: pointer.String("64"),
				Accelerator: map[string]string{
					v1.AcceleratorTypeKey:    string(v1.AcceleratorTypeNVIDIAGPU),
					v1.AcceleratorProductKey: "NVIDIA_A100",
				},
			},
			expectedResource: nil,
			wantErr:          false,
		},
		{
			name: "Convert NVIDIA GPU resource spec with none product to Ray",
			resourceInfo: &v1.ResourceSpec{
				GPU:    pointer.String("3"),
				CPU:    pointer.String("16"),
				Memory: pointer.String("64"),
				Accelerator: map[string]string{
					v1.AcceleratorTypeKey: string(v1.AcceleratorTypeNVIDIAGPU),
				},
			},
			expectedResource: &v1.RayResourceSpec{
				NumGPUs:   3,
				Resources: map[string]float64{
					// No custom resource for product
				},
			},
			wantErr: false,
		},
		{
			name:             "Convert nil resource spec",
			resourceInfo:     nil,
			expectedResource: nil,
			wantErr:          true,
		},
		{
			name: "Convert NVIDIA GPU resource spec with wrong accelerator type",
			resourceInfo: &v1.ResourceSpec{
				GPU:    pointer.String("2"),
				CPU:    pointer.String("8"),
				Memory: pointer.String("32"),
				Accelerator: map[string]string{
					v1.AcceleratorTypeKey:    string(v1.AcceleratorTypeAMDGPU),
					v1.AcceleratorProductKey: "AMD_Instinct_MI100",
				},
			},
			expectedResource: nil,
			wantErr:          false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			converter := NewGPUConverter()
			rayResource, err := converter.ConvertToRay(tt.resourceInfo)
			if (err != nil) != tt.wantErr {
				t.Errorf("ConvertToRay() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			equal, _, err := util.JsonEqual(rayResource, tt.expectedResource)
			if err != nil {
				t.Errorf("JsonEqual() error = %v", err)
				return
			}
			if !equal {
				t.Errorf("ConvertToRay() = %v, expected %v", rayResource, tt.expectedResource)
			}
		})
	}
}
