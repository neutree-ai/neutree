package accelerator

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestPluginImplementsPublicContract(t *testing.T) {
	var _ Plugin = contractPlugin{}
	var _ PluginHandle = contractPlugin{}
	var _ ResourceConverter = contractPlugin{}
	var _ ResourceParser = contractPlugin{}
	var _ StaticNodeRuntimeConfigResolver = contractPlugin{}
}

type contractPlugin struct{}

func (contractPlugin) Resource() string { return "contract-test" }
func (contractPlugin) Type() string     { return InternalPluginType }
func (p contractPlugin) Handle() PluginHandle {
	return p
}
func (contractPlugin) GetNodeAccelerator(context.Context, *v1.GetNodeAcceleratorRequest) (*v1.GetNodeAcceleratorResponse, error) {
	return nil, nil
}
func (contractPlugin) GetNodeRuntimeConfig(context.Context, *v1.GetNodeRuntimeConfigRequest) (*v1.GetNodeRuntimeConfigResponse, error) {
	return nil, nil
}
func (contractPlugin) DetectStaticNodeAccelerator(context.Context, *v1.DetectStaticNodeAcceleratorRequest) (*v1.DetectStaticNodeAcceleratorResponse, error) {
	return nil, nil
}
func (contractPlugin) GetContainerRuntimeConfig() (v1.RuntimeConfig, error) {
	return v1.RuntimeConfig{}, nil
}
func (contractPlugin) GetAcceleratorProfile(context.Context) (*v1.AcceleratorProfile, error) {
	return nil, nil
}
func (contractPlugin) GetStaticNodeRuntimeConfig(context.Context, *v1.StaticNodeAcceleratorStatus) (*v1.RuntimeConfig, bool, error) {
	return nil, false, nil
}
func (p contractPlugin) GetResourceConverter() ResourceConverter { return p }
func (p contractPlugin) GetResourceParser() ResourceParser       { return p }
func (contractPlugin) ConvertToRay(ConvertInput) (*v1.RayResourceSpec, error) {
	return nil, nil
}
func (contractPlugin) ConvertToKubernetes(ConvertInput) (*v1.KubernetesResourceSpec, error) {
	return nil, nil
}
func (contractPlugin) ParseFromRay(map[string]float64) (*v1.ResourceInfo, error) {
	return nil, nil
}
func (contractPlugin) ParseFromKubernetes(map[corev1.ResourceName]resource.Quantity, map[string]string) (*v1.ResourceInfo, error) {
	return nil, nil
}
