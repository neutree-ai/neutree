package resource

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
)

func TestConverterManager_RegisterConverter(t *testing.T) {
	cm := NewConverterManager()

	converter := plugin.NewGPUConverter()
	err := cm.RegisterConverter(v1.AcceleratorTypeNVIDIAGPU, converter)
	assert.NoError(t, err)

	types := cm.ListConverterTypes()
	assert.Contains(t, types, v1.AcceleratorTypeNVIDIAGPU)
}

func TestConverterManager_ConvertToRay_NVIDIA(t *testing.T) {
	cm := NewConverterManager()

	converter := plugin.NewGPUConverter()
	err := cm.RegisterConverter(v1.AcceleratorTypeNVIDIAGPU, converter)
	require.NoError(t, err)

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

	ray, err := cm.ConvertToRay(context.Background(), spec)
	require.NoError(t, err)
	assert.NotNil(t, ray)
	assert.Equal(t, float64(2), ray.NumGPUs)
	assert.Equal(t, float64(16), ray.NumCPUs)
	assert.Equal(t, float64(64*plugin.BytesPerGiB), ray.Memory)
	assert.Equal(t, float64(2), ray.Resources["NVIDIA-L20"])
	assert.Equal(t, float64(2), ray.Resources["rdma/hca"])
}

func TestConverterManager_ConvertToKubernetes_NVIDIA(t *testing.T) {
	cm := NewConverterManager()

	converter := plugin.NewGPUConverter()
	err := cm.RegisterConverter(v1.AcceleratorTypeNVIDIAGPU, converter)
	require.NoError(t, err)

	gpu := float64(1)
	cpu := float64(8)
	memory := float64(32)
	spec := &v1.ResourceSpec{
		GPU:    &gpu,
		CPU:    &cpu,
		Memory: &memory,
	}
	spec.SetAcceleratorType(v1.AcceleratorTypeNVIDIAGPU)
	spec.SetAcceleratorProduct("NVIDIA-L20")

	// 转换为 Kubernetes
	k8s, err := cm.ConvertToKubernetes(context.Background(), spec)
	require.NoError(t, err)
	assert.NotNil(t, k8s)
	assert.Equal(t, "1", k8s.Requests["nvidia.com/gpu"])
	assert.Equal(t, "1", k8s.Limits["nvidia.com/gpu"])
	assert.Equal(t, "8", k8s.Requests["cpu"])
	assert.Equal(t, "32Gi", k8s.Requests["memory"])
	assert.Equal(t, "NVIDIA-L20", k8s.NodeSelector["nvidia.com/gpu.product"])
}

func TestConverterManager_ConvertToRay_AMD(t *testing.T) {
	cm := NewConverterManager()

	converter := plugin.NewAMDGPUConverter()
	err := cm.RegisterConverter(v1.AcceleratorTypeAMDGPU, converter)
	require.NoError(t, err)

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

	ray, err := cm.ConvertToRay(context.Background(), spec)
	require.NoError(t, err)
	assert.NotNil(t, ray)
	assert.Equal(t, float64(1), ray.NumGPUs)
	assert.Equal(t, float64(8), ray.NumCPUs)
	assert.Equal(t, float64(1), ray.Resources["AMD_Instinct_MI300X_VF"])
}

func TestConverterManager_ConvertToKubernetes_AMD(t *testing.T) {
	cm := NewConverterManager()

	converter := plugin.NewAMDGPUConverter()
	err := cm.RegisterConverter(v1.AcceleratorTypeAMDGPU, converter)
	require.NoError(t, err)

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

	k8s, err := cm.ConvertToKubernetes(context.Background(), spec)
	require.NoError(t, err)
	assert.NotNil(t, k8s)
	assert.Equal(t, "1", k8s.Requests["amd.com/gpu"])
	assert.Equal(t, "1", k8s.Limits["amd.com/gpu"])
	assert.Equal(t, "AMD_Instinct_MI300X_VF", k8s.NodeSelector["amd.com/gpu.product-name"])
	assert.Equal(t, "1024Mi", k8s.Requests["hugepages-2Mi"])
}

func TestConverterManager_CPUOnly(t *testing.T) {
	cm := NewConverterManager()

	cpu := float64(4)
	memory := float64(8)
	spec := &v1.ResourceSpec{
		CPU:    &cpu,
		Memory: &memory,
	}

	ray, err := cm.ConvertToRay(context.Background(), spec)
	require.NoError(t, err)
	assert.NotNil(t, ray)
	assert.Equal(t, float64(0), ray.NumGPUs)
	assert.Equal(t, float64(4), ray.NumCPUs)
	assert.Equal(t, float64(8*plugin.BytesPerGiB), ray.Memory)

	k8s, err := cm.ConvertToKubernetes(context.Background(), spec)
	require.NoError(t, err)
	assert.NotNil(t, k8s)
	assert.Equal(t, "4", k8s.Requests["cpu"])
	assert.Equal(t, "8Gi", k8s.Requests["memory"])
}

func TestConverterManager_CPUOnly_WithCustomResources(t *testing.T) {
	cm := NewConverterManager()

	cpu := float64(8)
	memory := float64(16)
	spec := &v1.ResourceSpec{
		CPU:    &cpu,
		Memory: &memory,
	}
	spec.AddCustomResource("hugepages-2Mi", "1024Mi")
	spec.AddCustomResource("rdma/hca", "1")

	ray, err := cm.ConvertToRay(context.Background(), spec)
	require.NoError(t, err)
	assert.NotNil(t, ray)
	assert.Equal(t, float64(0), ray.NumGPUs)
	assert.Equal(t, float64(8), ray.NumCPUs)
	assert.Equal(t, float64(16*plugin.BytesPerGiB), ray.Memory)
	// can not convert to float64, so should be 0
	assert.Equal(t, float64(0), ray.Resources["hugepages-2Mi"])
	assert.Equal(t, float64(1), ray.Resources["rdma/hca"])

	k8s, err := cm.ConvertToKubernetes(context.Background(), spec)
	require.NoError(t, err)
	assert.NotNil(t, k8s)
	assert.Equal(t, "8", k8s.Requests["cpu"])
	assert.Equal(t, "16Gi", k8s.Requests["memory"])
	assert.Equal(t, "1024Mi", k8s.Requests["hugepages-2Mi"])
	assert.Equal(t, "1", k8s.Requests["rdma/hca"])
}

func TestConverterManager_CPUOnly_MinimalConfig(t *testing.T) {
	cm := NewConverterManager()

	spec := &v1.ResourceSpec{}

	ray, err := cm.ConvertToRay(context.Background(), spec)
	require.NoError(t, err)
	assert.NotNil(t, ray)
	assert.Equal(t, float64(0), ray.NumGPUs)
	assert.Equal(t, float64(0), ray.NumCPUs)
	assert.Equal(t, float64(0), ray.Memory)

	k8s, err := cm.ConvertToKubernetes(context.Background(), spec)
	require.NoError(t, err)
	assert.NotNil(t, k8s)
	assert.Empty(t, k8s.Requests)
	assert.Empty(t, k8s.Limits)
}

func TestConverterManager_CPUOnly_OnlyCPU(t *testing.T) {
	cm := NewConverterManager()

	cpu := float64(2)
	spec := &v1.ResourceSpec{
		CPU: &cpu,
	}

	ray, err := cm.ConvertToRay(context.Background(), spec)
	require.NoError(t, err)
	assert.NotNil(t, ray)
	assert.Equal(t, float64(2), ray.NumCPUs)
	assert.Equal(t, float64(0), ray.Memory)

	k8s, err := cm.ConvertToKubernetes(context.Background(), spec)
	require.NoError(t, err)
	assert.NotNil(t, k8s)
	assert.Equal(t, "2", k8s.Requests["cpu"])
	assert.Empty(t, k8s.Requests["memory"])
}

func TestConverterManager_CPUOnly_OnlyMemory(t *testing.T) {
	cm := NewConverterManager()

	// 只配置内存
	memory := float64(16)
	spec := &v1.ResourceSpec{
		Memory: &memory,
	}

	// 转换为 Ray
	ray, err := cm.ConvertToRay(context.Background(), spec)
	require.NoError(t, err)
	assert.NotNil(t, ray)
	assert.Equal(t, float64(0), ray.NumCPUs)
	assert.Equal(t, float64(16*plugin.BytesPerGiB), ray.Memory)

	// 转换为 Kubernetes
	k8s, err := cm.ConvertToKubernetes(context.Background(), spec)
	require.NoError(t, err)
	assert.NotNil(t, k8s)
	assert.Equal(t, "16Gi", k8s.Requests["memory"])
	assert.Empty(t, k8s.Requests["cpu"])
}

func TestConverterManager_GPUZero_NoAcceleratorType(t *testing.T) {
	cm := NewConverterManager()

	gpu := float64(0)
	cpu := float64(4)
	memory := float64(8)
	spec := &v1.ResourceSpec{
		GPU:    &gpu,
		CPU:    &cpu,
		Memory: &memory,
	}

	ray, err := cm.ConvertToRay(context.Background(), spec)
	require.NoError(t, err)
	assert.NotNil(t, ray)
	assert.Equal(t, float64(0), ray.NumGPUs)
	assert.Equal(t, float64(4), ray.NumCPUs)
	assert.Equal(t, float64(8*plugin.BytesPerGiB), ray.Memory)

	k8s, err := cm.ConvertToKubernetes(context.Background(), spec)
	require.NoError(t, err)
	assert.NotNil(t, k8s)
	assert.Equal(t, "4", k8s.Requests["cpu"])
	assert.Equal(t, "8Gi", k8s.Requests["memory"])
}

func TestConverterManager_NoConverterFound(t *testing.T) {
	cm := NewConverterManager()

	gpu := float64(1)
	spec := &v1.ResourceSpec{
		GPU: &gpu,
	}
	spec.SetAcceleratorType("unknown_gpu")

	_, err := cm.ConvertToRay(context.Background(), spec)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no converter found")

	_, err = cm.ConvertToKubernetes(context.Background(), spec)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no converter found")
}
