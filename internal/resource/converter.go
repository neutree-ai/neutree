package resource

import (
	"context"
	"fmt"
	"strconv"

	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator"
	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
)

type converter struct {
	pluginRegistry accelerator.PluginRegistry
}

func newConverter(pluginRegistry accelerator.PluginRegistry) *converter {
	return &converter{
		pluginRegistry: pluginRegistry,
	}
}

func (c *converter) ConvertToRay(ctx context.Context, spec *v1.ResourceSpec) (*v1.RayResourceSpec, error) {
	if spec == nil {
		return nil, fmt.Errorf("resource spec cannot be nil")
	}

	acceleratorType := spec.GetAcceleratorType()

	if acceleratorType == "" {
		klog.V(4).InfoS("No accelerator type specified, using CPU-only configuration")
		return c.convertCPUOnlyToRay(spec), nil
	}

	klog.V(4).InfoS("Converting resource spec to Ray",
		"acceleratorType", acceleratorType,
		"gpu", spec.GPU,
		"cpu", spec.CPU,
	)

	converter, ok := c.pluginRegistry.GetConverter(acceleratorType)
	if !ok {
		err := fmt.Errorf("no converter found for accelerator type: %s", acceleratorType)
		klog.ErrorS(err, "Conversion failed",
			"acceleratorType", acceleratorType,
		)

		return nil, err
	}

	result, err := converter.ConvertToRay(spec)
	if err != nil {
		klog.ErrorS(err, "Converter execution failed",
			"acceleratorType", acceleratorType,
			"spec", spec,
		)

		return nil, fmt.Errorf("conversion failed for %s: %w", acceleratorType, err)
	}

	klog.V(4).InfoS("Conversion successful",
		"acceleratorType", acceleratorType,
		"numGPUs", result.NumGPUs,
		"numCPUs", result.NumCPUs,
	)

	return result, nil
}

func (c *converter) ConvertToKubernetes(ctx context.Context, spec *v1.ResourceSpec) (*v1.KubernetesResourceSpec, error) {
	if spec == nil {
		return nil, fmt.Errorf("resource spec cannot be nil")
	}

	acceleratorType := spec.GetAcceleratorType()

	if acceleratorType == "" {
		klog.V(4).InfoS("No accelerator type specified, using CPU-only configuration")
		return c.convertCPUOnlyToKubernetes(spec), nil
	}

	klog.V(4).InfoS("Converting resource spec to Kubernetes",
		"acceleratorType", acceleratorType,
		"gpu", spec.GPU,
		"cpu", spec.CPU,
	)

	converter, ok := c.pluginRegistry.GetConverter(acceleratorType)
	if !ok {
		err := fmt.Errorf("no converter found for accelerator type: %s", acceleratorType)
		klog.ErrorS(err, "Conversion failed",
			"acceleratorType", acceleratorType,
		)

		return nil, err
	}

	result, err := converter.ConvertToKubernetes(spec)
	if err != nil {
		klog.ErrorS(err, "Converter execution failed",
			"acceleratorType", acceleratorType,
			"spec", spec,
		)

		return nil, fmt.Errorf("conversion failed for %s: %w", acceleratorType, err)
	}

	klog.V(4).InfoS("Conversion successful",
		"acceleratorType", acceleratorType,
		"requests", result.Requests,
	)

	return result, nil
}

func (c *converter) convertCPUOnlyToRay(spec *v1.ResourceSpec) *v1.RayResourceSpec {
	ray := &v1.RayResourceSpec{
		Resources: make(map[string]float64),
	}

	if spec.CPU != nil {
		ray.NumCPUs = *spec.CPU
	}

	if spec.Memory != nil {
		ray.Memory = *spec.Memory * plugin.BytesPerGiB
	}

	for k, v := range spec.GetCustomResources() {
		if floatVal, err := strconv.ParseFloat(v, 64); err == nil {
			ray.Resources[k] = floatVal
		} else {
			klog.Warningf("Failed to parse custom resource %s value %s to float: %v", k, v, err)
		}
	}

	return ray
}

func (c *converter) convertCPUOnlyToKubernetes(spec *v1.ResourceSpec) *v1.KubernetesResourceSpec {
	k8s := &v1.KubernetesResourceSpec{
		Requests:     make(map[string]string),
		Limits:       make(map[string]string),
		NodeSelector: make(map[string]string),
	}

	if spec.CPU != nil && *spec.CPU > 0 {
		cpuStr := fmt.Sprintf("%.0f", *spec.CPU)
		k8s.Requests["cpu"] = cpuStr
		k8s.Limits["cpu"] = cpuStr
	}

	if spec.Memory != nil && *spec.Memory > 0 {
		memoryStr := fmt.Sprintf("%.0fGi", *spec.Memory)
		k8s.Requests["memory"] = memoryStr
		k8s.Limits["memory"] = memoryStr
	}

	for k, v := range spec.GetCustomResources() {
		k8s.Requests[k] = v
		k8s.Limits[k] = v
	}

	return k8s
}
