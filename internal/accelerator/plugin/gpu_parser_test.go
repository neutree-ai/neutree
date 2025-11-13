package plugin

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
)

func TestGPUParser_ParserFromRay(t *testing.T) {
	parser := &GPUResourceParser{}

	tests := []struct {
		name     string
		resource map[string]float64
		expected *v1.ResourceInfo
		wantErr  bool
	}{
		{
			name: "Valid NVIDIA GPU resources",
			resource: map[string]float64{
				"GPU":               4,
				"NVIDIA_TESLA_V100": 4,
			},
			expected: &v1.ResourceInfo{
				AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
					"nvidia_gpu": {
						Quantity: 4,
						ProductGroups: map[v1.AcceleratorProduct]float64{
							"NVIDIA_TESLA_V100": 4,
						},
					},
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parser.ParseFromRay(tt.resource)
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
