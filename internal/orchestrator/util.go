package orchestrator

import (
	"fmt"
	"strconv"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator"
	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
	"github.com/neutree-ai/neutree/pkg/storage"
)

func getEndpointDeployCluster(s storage.Storage, endpoint *v1.Endpoint) (*v1.Cluster, error) { //nolint:unparam
	clusterFilter := []storage.Filter{
		{
			Column:   "metadata->name",
			Operator: "eq",
			Value:    fmt.Sprintf(`"%s"`, endpoint.Spec.Cluster),
		},
	}

	if endpoint.Metadata.Workspace != "" {
		clusterFilter = append(clusterFilter, storage.Filter{
			Column:   "metadata->workspace",
			Operator: "eq",
			Value:    fmt.Sprintf(`"%s"`, endpoint.Metadata.Workspace),
		})
	}

	clusterList, err := s.ListCluster(storage.ListOption{Filters: clusterFilter})
	if err != nil {
		return nil, errors.Wrap(err, "failed to list cluster")
	}

	if len(clusterList) == 0 {
		return nil, storage.ErrResourceNotFound
	}

	return &clusterList[0], nil
}

func getUsedEngine(s storage.Storage, endpoint *v1.Endpoint) (*v1.Engine, error) {
	engine, err := s.ListEngine(storage.ListOption{
		Filters: []storage.Filter{
			{
				Column:   "metadata->name",
				Operator: "eq",
				Value:    strconv.Quote(endpoint.Spec.Engine.Engine),
			},
		},
	})
	if err != nil {
		return nil, errors.Wrap(err, "failed to list engine")
	}

	if len(engine) == 0 {
		return nil, errors.New("engine " + endpoint.Spec.Engine.Engine + " not found")
	}

	if engine[0].Status == nil || engine[0].Status.Phase != v1.EnginePhaseCreated {
		return nil, errors.New("engine " + endpoint.Spec.Engine.Engine + " not ready")
	}

	versionMatched := false

	for _, v := range engine[0].Spec.Versions {
		if v.Version == endpoint.Spec.Engine.Version {
			versionMatched = true
			break
		}
	}

	if !versionMatched {
		return nil, errors.New("engine " + endpoint.Spec.Engine.Engine + " version " + endpoint.Spec.Engine.Version + " not found")
	}

	return &engine[0], nil
}

func getEndpointModelRegistry(s storage.Storage, endpoint *v1.Endpoint) (*v1.ModelRegistry, error) {
	modelRegistry, err := s.ListModelRegistry(storage.ListOption{
		Filters: []storage.Filter{
			{
				Column:   "metadata->name",
				Operator: "eq",
				Value:    strconv.Quote(endpoint.Spec.Model.Registry),
			},
			{
				Column:   "metadata->workspace",
				Operator: "eq",
				Value:    strconv.Quote(endpoint.Metadata.Workspace),
			},
		},
	})
	if err != nil {
		return nil, errors.Wrap(err, "failed to list model registry")
	}

	if len(modelRegistry) == 0 {
		return nil, storage.ErrResourceNotFound
	}

	if modelRegistry[0].Status == nil || modelRegistry[0].Status.Phase != v1.ModelRegistryPhaseCONNECTED {
		return nil, errors.New("model registry " + endpoint.Spec.Model.Registry + " not ready")
	}

	return &modelRegistry[0], nil
}

func getUsedImageRegistries(cluster *v1.Cluster, s storage.Storage) (*v1.ImageRegistry, error) {
	imageRegistryFilter := []storage.Filter{
		{
			Column:   "metadata->name",
			Operator: "eq",
			Value:    fmt.Sprintf(`"%s"`, cluster.Spec.ImageRegistry),
		},
	}

	if cluster.Metadata.Workspace != "" {
		imageRegistryFilter = append(imageRegistryFilter, storage.Filter{
			Column:   "metadata->workspace",
			Operator: "eq",
			Value:    fmt.Sprintf(`"%s"`, cluster.Metadata.Workspace),
		})
	}

	imageRegistryList, err := s.ListImageRegistry(storage.ListOption{Filters: imageRegistryFilter})
	if err != nil {
		return nil, errors.Wrap(err, "failed to list image registry")
	}

	if len(imageRegistryList) == 0 {
		return nil, storage.ErrResourceNotFound
	}

	return &imageRegistryList[0], nil
}

func convertToRay(acceleratorMgr accelerator.Manager, spec *v1.ResourceSpec) (*v1.RayResourceSpec, error) {
	if spec == nil {
		return nil, fmt.Errorf("resource spec cannot be nil")
	}

	acceleratorType := spec.GetAcceleratorType()

	if acceleratorType == "" {
		klog.V(4).InfoS("No accelerator type specified, using CPU-only configuration")
		return convertCPUToRay(spec), nil
	}

	klog.V(4).InfoS("Converting resource spec to Ray",
		"acceleratorType", acceleratorType,
		"gpu", spec.GPU,
		"cpu", spec.CPU,
		"memory", spec.Memory,
	)

	result := convertCPUToRay(spec)

	converter, ok := acceleratorMgr.GetConverter(acceleratorType)
	if !ok {
		err := fmt.Errorf("no converter found for accelerator type: %s", acceleratorType)
		klog.ErrorS(err, "Conversion failed",
			"acceleratorType", acceleratorType,
		)

		return nil, err
	}

	acceleratorResult, err := converter.ConvertToRay(spec)
	if err != nil {
		klog.ErrorS(err, "Converter execution failed",
			"acceleratorType", acceleratorType,
			"spec", spec,
		)

		return nil, fmt.Errorf("conversion failed for %s: %w", acceleratorType, err)
	}

	// Merge accelerator-specific Ray resources
	result.NumGPUs += acceleratorResult.NumGPUs
	for k, v := range acceleratorResult.Resources {
		result.Resources[k] = v
	}

	// Add custom resources
	for k, v := range spec.GetCustomResources() {
		// Try to convert to number
		if floatVal, err := strconv.ParseFloat(v, 64); err == nil {
			result.Resources[k] = floatVal
		} else {
			klog.Warningf("Failed to parse custom resource %s value %s to float: %v", k, v, err)
		}
	}

	klog.V(4).InfoS("Conversion successful",
		"acceleratorType", acceleratorType,
		"numGPUs", result.NumGPUs,
		"numCPUs", result.NumCPUs,
		"memory", result.Memory,
		"resources", result.Resources,
	)

	return result, nil
}

func convertToKubernetes(acceleratorMgr accelerator.Manager, spec *v1.ResourceSpec) (*v1.KubernetesResourceSpec, error) {
	if spec == nil {
		return nil, fmt.Errorf("resource spec cannot be nil")
	}

	acceleratorType := spec.GetAcceleratorType()

	if acceleratorType == "" {
		klog.V(4).InfoS("No accelerator type specified, using CPU-only configuration")
		return convertCPUToKubernetes(spec), nil
	}

	klog.V(4).InfoS("Converting resource spec to Kubernetes",
		"acceleratorType", acceleratorType,
		"gpu", spec.GPU,
		"cpu", spec.CPU,
	)

	result := convertCPUToKubernetes(spec)

	converter, ok := acceleratorMgr.GetConverter(acceleratorType)
	if !ok {
		err := fmt.Errorf("no converter found for accelerator type: %s", acceleratorType)
		klog.ErrorS(err, "Conversion failed",
			"acceleratorType", acceleratorType,
		)

		return nil, err
	}

	acceleratorResult, err := converter.ConvertToKubernetes(spec)
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

	// Merge accelerator-specific Kubernetes resources
	for k, v := range acceleratorResult.Requests {
		result.Requests[k] = v
	}

	for k, v := range acceleratorResult.Limits {
		result.Limits[k] = v
	}

	for k, v := range acceleratorResult.NodeSelector {
		result.NodeSelector[k] = v
	}

	// Add custom resources
	for k, v := range spec.GetCustomResources() {
		result.Requests[k] = v
		result.Limits[k] = v
	}

	return result, nil
}

func convertCPUToRay(spec *v1.ResourceSpec) *v1.RayResourceSpec {
	res := &v1.RayResourceSpec{
		Resources: make(map[string]float64),
	}

	if spec.CPU != nil {
		res.NumCPUs = *spec.CPU
	}

	if spec.Memory != nil {
		res.Memory = *spec.Memory * plugin.BytesPerGiB
	}

	return res
}

func convertCPUToKubernetes(spec *v1.ResourceSpec) *v1.KubernetesResourceSpec {
	res := &v1.KubernetesResourceSpec{
		Requests:     make(map[string]string),
		Limits:       make(map[string]string),
		NodeSelector: make(map[string]string),
	}

	if spec.CPU != nil && *spec.CPU > 0 {
		cpuStr := fmt.Sprintf("%.0f", *spec.CPU)
		res.Requests["cpu"] = cpuStr
		res.Limits["cpu"] = cpuStr
	}

	if spec.Memory != nil && *spec.Memory > 0 {
		memoryStr := fmt.Sprintf("%.0fGi", *spec.Memory)
		res.Requests["memory"] = memoryStr
		res.Limits["memory"] = memoryStr
	}

	return res
}
