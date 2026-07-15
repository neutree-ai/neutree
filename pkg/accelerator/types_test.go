package accelerator

import (
	"context"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestExternalPluginImplementsPublicContract(t *testing.T) {
	var _ Plugin = externalPlugin{}
	var _ PluginHandle = externalPlugin{}
	var _ ResourceConverter = externalPlugin{}
	var _ ResourceParser = externalPlugin{}
}

type externalPlugin struct{}

func (externalPlugin) Resource() string { return "external-test" }
func (externalPlugin) Type() string     { return InternalPluginType }
func (p externalPlugin) Handle() PluginHandle {
	return p
}
func (externalPlugin) GetNodeAccelerator(context.Context, *v1.GetNodeAcceleratorRequest) (*v1.GetNodeAcceleratorResponse, error) {
	return nil, nil
}
func (externalPlugin) GetNodeRuntimeConfig(context.Context, *v1.GetNodeRuntimeConfigRequest) (*v1.GetNodeRuntimeConfigResponse, error) {
	return nil, nil
}
func (externalPlugin) DetectStaticNodeAccelerator(context.Context, *v1.DetectStaticNodeAcceleratorRequest) (*v1.DetectStaticNodeAcceleratorResponse, error) {
	return nil, nil
}
func (externalPlugin) GetContainerRuntimeConfig() (v1.RuntimeConfig, error) {
	return v1.RuntimeConfig{}, nil
}
func (externalPlugin) GetAcceleratorProfile(context.Context) (*v1.AcceleratorProfile, error) {
	return nil, nil
}
func (p externalPlugin) GetResourceConverter() ResourceConverter { return p }
func (p externalPlugin) GetResourceParser() ResourceParser       { return p }
func (externalPlugin) Ping(context.Context) error                { return nil }
func (externalPlugin) ConvertToRay(*v1.ResourceSpec) (*v1.RayResourceSpec, error) {
	return nil, nil
}
func (externalPlugin) ConvertToKubernetes(*v1.ResourceSpec) (*v1.KubernetesResourceSpec, error) {
	return nil, nil
}
func (externalPlugin) ParseFromRay(map[string]float64) (*v1.ResourceInfo, error) {
	return nil, nil
}
func (externalPlugin) ParseFromKubernetes(map[corev1.ResourceName]resource.Quantity, map[string]string) (*v1.ResourceInfo, error) {
	return nil, nil
}
