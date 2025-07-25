package accelerator

import (
	"context"
	"sync"
	"testing"
	"time"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestManager_registerAcceleratorPlugin(t *testing.T) {
	manager := &manager{}

	// Test registering a new plugin
	resourceName := "test"
	p := plugin.NewAcceleratorRestPlugin(resourceName, "http://127.0.0.1:80")
	manager.registerAcceleratorPlugin(p)
	value, ok := manager.acceleratorsMap.Load(resourceName)
	rp := value.(registerPlugin)
	assert.True(t, ok)
	assert.Equal(t, p, rp.plugin)
	rt := rp.lastRegisterTime

	// Test registering an existing plugin
	manager.registerAcceleratorPlugin(p)
	value, ok = manager.acceleratorsMap.Load(resourceName)
	assert.True(t, ok)
	rp, ok = value.(registerPlugin)
	assert.True(t, ok)
	assert.NotEqual(t, rt, rp.lastRegisterTime)
}

func TestManager_GetKubernetesContainerRuntimeConfig(t *testing.T) {
	tests := []struct {
		name                  string
		acceleratorType       string
		container             corev1.Container
		setup                 func(*manager)
		expectedRuntimeConfig v1.RuntimeConfig
		wantErr               bool
	}{
		{
			name:            "empty accelerator type",
			acceleratorType: "",
			container:       corev1.Container{},
			expectedRuntimeConfig: v1.RuntimeConfig{
				Env: map[string]string{
					"NVIDIA_VISIBLE_DEVICES": "void",
				},
			},
			wantErr: false,
		},
		{
			name:            "accelerator plugin not found",
			acceleratorType: "gpu",
			container:       corev1.Container{},
			setup: func(m *manager) {
				m.acceleratorsMap = sync.Map{}
			},
			wantErr: true,
		},
		{
			name:            "success get container runtime config",
			acceleratorType: "gpu",
			setup: func(m *manager) {
				m.acceleratorsMap = sync.Map{}
				gpuPlugin := &plugin.GPUAcceleratorPlugin{}
				m.acceleratorsMap.Store("gpu", registerPlugin{
					resource:         gpuPlugin.Resource(),
					plugin:           gpuPlugin,
					lastRegisterTime: time.Now(),
				})
			},
			container: corev1.Container{
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{
						"nvidia.com/gpu": *resource.NewQuantity(1, resource.DecimalSI),
					},
					Requests: corev1.ResourceList{
						"nvidia.com/gpu": *resource.NewQuantity(1, resource.DecimalSI),
					},
				},
			},
			expectedRuntimeConfig: v1.RuntimeConfig{
				Env: map[string]string{
					"ACCELERATOR_TYPE": "gpu",
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := &manager{}
			if tt.setup != nil {
				tt.setup(manager)
			}
			resp, err := manager.GetKubernetesContainerRuntimeConfig(context.Background(), tt.acceleratorType, tt.container)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedRuntimeConfig, resp)
			}
		})
	}
}
