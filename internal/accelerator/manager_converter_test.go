package accelerator

import (
	"context"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
)

func TestManager_ConvertToRay(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	manager := NewManager(engine)

	gpu := float64(2)
	cpu := float64(16)
	memory := float64(64)
	spec := &v1.ResourceSpec{
		GPU:    &gpu,
		CPU:    &cpu,
		Memory: &memory,
	}
	spec.SetAcceleratorType(v1.AcceleratorTypeNVIDIAGPU)
	spec.SetAcceleratorProduct("NVIDIA-L20")
	spec.AddCustomResource("rdma/hca", "2")

	// 转换为 Ray
	ray, err := manager.ConvertToRay(context.Background(), spec)
	require.NoError(t, err)
	assert.NotNil(t, ray)
	assert.Equal(t, float64(2), ray.NumGPUs)
	assert.Equal(t, float64(16), ray.NumCPUs)
	assert.Equal(t, float64(64*plugin.BytesPerGiB), ray.Memory)
	assert.Equal(t, float64(2), ray.Resources["NVIDIA-L20"])
	assert.Equal(t, float64(2), ray.Resources["rdma/hca"])
}

func TestManager_ConvertToKubernetes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	manager := NewManager(engine)

	gpu := float64(1)
	cpu := float64(8)
	memory := float64(32)
	spec := &v1.ResourceSpec{
		GPU:    &gpu,
		CPU:    &cpu,
		Memory: &memory,
	}
	spec.SetAcceleratorType(v1.AcceleratorTypeAMDGPU)
	spec.SetAcceleratorProduct("AMD_Instinct_MI300X_VF")
	spec.AddCustomResource("hugepages-2Mi", "1024Mi")

	// 转换为 Kubernetes
	k8s, err := manager.ConvertToKubernetes(context.Background(), spec)
	require.NoError(t, err)
	assert.NotNil(t, k8s)
	assert.Equal(t, "1", k8s.Requests["amd.com/gpu"])
	assert.Equal(t, "1", k8s.Limits["amd.com/gpu"])
	assert.Equal(t, "8", k8s.Requests["cpu"])
	assert.Equal(t, "32Gi", k8s.Requests["memory"])
	assert.Equal(t, "AMD_Instinct_MI300X_VF", k8s.NodeSelector["amd.com/gpu.product-name"])
	assert.Equal(t, "1024Mi", k8s.Requests["hugepages-2Mi"])
}

func TestManager_ConvertCPUOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	manager := NewManager(engine)

	cpu := float64(4)
	memory := float64(8)
	spec := &v1.ResourceSpec{
		CPU:    &cpu,
		Memory: &memory,
	}

	ray, err := manager.ConvertToRay(context.Background(), spec)
	require.NoError(t, err)
	assert.NotNil(t, ray)
	assert.Equal(t, float64(0), ray.NumGPUs)
	assert.Equal(t, float64(4), ray.NumCPUs)
	assert.Equal(t, float64(8*plugin.BytesPerGiB), ray.Memory)

	k8s, err := manager.ConvertToKubernetes(context.Background(), spec)
	require.NoError(t, err)
	assert.NotNil(t, k8s)
	assert.Equal(t, "4", k8s.Requests["cpu"])
	assert.Equal(t, "8Gi", k8s.Requests["memory"])
}
