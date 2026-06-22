package proxies

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

func validateEndpointAcceleratorVirtualization(clusterStorage storage.ClusterStorage) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method != http.MethodPost && c.Request.Method != http.MethodPatch {
			c.Next()
			return
		}

		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.JSON(http.StatusBadRequest, &validationError{
				Code:    "10214",
				Message: "invalid endpoint payload",
				Hint:    err.Error(),
			})
			c.Abort()

			return
		}

		c.Request.Body = io.NopCloser(bytes.NewReader(body))

		if len(bytes.TrimSpace(body)) == 0 {
			c.Next()
			return
		}

		endpoint, validationErr := parseEndpointAcceleratorVirtualizationBody(body)
		if validationErr != nil {
			c.JSON(http.StatusBadRequest, validationErr)
			c.Abort()

			return
		}

		if c.Request.Method == http.MethodPost {
			validationErr = validateEndpointAcceleratorVirtualizationCreateCapacity(clusterStorage, endpoint)
		}

		if validationErr != nil {
			c.JSON(http.StatusBadRequest, validationErr)
			c.Abort()

			return
		}

		c.Next()
	}
}

func validateEndpointAcceleratorVirtualizationBody(body []byte) *validationError {
	_, err := parseEndpointAcceleratorVirtualizationBody(body)

	return err
}

func parseEndpointAcceleratorVirtualizationBody(body []byte) (*v1.Endpoint, *validationError) {
	var endpoint v1.Endpoint
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&endpoint); err != nil {
		return nil, invalidEndpointPayloadError(err)
	}

	if endpoint.GetDeletionTimestamp() != "" {
		// Soft delete PATCH reuses the same route but should not be blocked by
		// endpoint resource validation.
		return &endpoint, nil
	}

	if endpoint.Spec == nil || endpoint.Spec.Resources == nil {
		return &endpoint, nil
	}

	resources := endpoint.Spec.Resources
	if err := validateEndpointAcceleratorVirtualizationResourceShape(resources); err != nil {
		return nil, err
	}

	return &endpoint, nil
}

func validateEndpointAcceleratorVirtualizationResourceShape(resources *v1.ResourceSpec) *validationError {
	if !resources.HasAcceleratorVirtualization() {
		return nil
	}

	if resources.GetAcceleratorType() != string(v1.AcceleratorTypeNVIDIAGPU) {
		return &validationError{
			Code:    "10217",
			Message: "accelerator virtualization is only supported for NVIDIA GPU endpoints",
			Hint:    "Set spec.resources.accelerator.type to nvidia_gpu",
		}
	}

	if resources.GetAcceleratorProduct() == "" {
		return &validationError{
			Code:    "10218",
			Message: "endpoint accelerator virtualization requires accelerator product",
			Hint:    "Set spec.resources.accelerator.product to the target GPU product",
		}
	}

	if resources.GetAcceleratorVirtualizationMemoryMiB() != "" && resources.GetAcceleratorVirtualizationMemoryPercent() != "" {
		return &validationError{
			Code:    "10219",
			Message: "virtualization memory_mib and memory_percent are mutually exclusive",
			Hint:    "Set only one of virtualization.memory_mib or virtualization.memory_percent",
		}
	}

	if _, err := parsePositiveIntegerResource(resources.GPU, "spec.resources.gpu"); err != nil {
		return endpointResourceValueError(err)
	}

	if _, err := parseOptionalPositiveInteger(resources.GetAcceleratorVirtualizationMemoryMiB(), "virtualization.memory_mib"); err != nil {
		return endpointResourceValueError(err)
	}

	if err := validatePercentResource(resources.GetAcceleratorVirtualizationMemoryPercent(), "virtualization.memory_percent"); err != nil {
		return endpointResourceValueError(err)
	}

	if err := validatePercentResource(resources.GetAcceleratorVirtualizationCorePercent(), "virtualization.core_percent"); err != nil {
		return endpointResourceValueError(err)
	}

	return nil
}

func validateEndpointAcceleratorVirtualizationCreateCapacity(clusterStorage storage.ClusterStorage, endpoint *v1.Endpoint) *validationError {
	if endpoint == nil || endpoint.GetDeletionTimestamp() != "" || endpoint.Spec == nil || endpoint.Spec.Resources == nil ||
		!endpoint.Spec.Resources.HasAcceleratorVirtualization() {
		return nil
	}

	if clusterStorage == nil {
		return endpointAcceleratorVirtualizationCapacityError("cluster storage is unavailable")
	}

	clusterName := endpoint.Spec.Cluster
	if clusterName == "" {
		return endpointAcceleratorVirtualizationCapacityError("spec.cluster is required for accelerator virtualization capacity validation")
	}

	workspace := endpoint.GetWorkspace()
	if workspace == "" {
		workspace = defaultWorkspace
	}

	clusters, err := clusterStorage.ListCluster(storage.ListOption{
		Filters: []storage.Filter{
			{Column: "metadata->name", Operator: "eq", Value: strconv.Quote(clusterName)},
			{Column: "metadata->workspace", Operator: "eq", Value: strconv.Quote(workspace)},
		},
	})
	if err != nil {
		return endpointAcceleratorVirtualizationCapacityError(fmt.Sprintf("failed to look up cluster %s in workspace %s: %v", clusterName, workspace, err))
	}

	if len(clusters) == 0 {
		return endpointAcceleratorVirtualizationCapacityError(fmt.Sprintf("cluster %s not found in workspace %s", clusterName, workspace))
	}

	return validateEndpointAcceleratorVirtualizationCapacity(endpoint.Spec.Resources, &clusters[0])
}

func validateEndpointAcceleratorVirtualizationCapacity(resources *v1.ResourceSpec, cluster *v1.Cluster) *validationError {
	if resources == nil || !resources.HasAcceleratorVirtualization() {
		return nil
	}

	requestedGPU, err := parsePositiveIntegerResource(resources.GPU, "spec.resources.gpu")
	if err != nil {
		return endpointResourceValueError(err)
	}

	product := resources.GetAcceleratorProduct()
	resourceInfo := clusterResourceInfo(cluster)

	productResource := acceleratorVirtualizationProductResource(resourceInfo, product)
	if productResource == nil || productResource.Virtualization == nil {
		return endpointAcceleratorVirtualizationCapacityError(fmt.Sprintf("product=%s has no available accelerator virtualization capacity", product))
	}

	requestedMemoryMiB, err := requestedAcceleratorVirtualizationMemoryMiB(resources, resourceInfo, product)
	if err != nil {
		return endpointAcceleratorVirtualizationCapacityError(err.Error())
	}

	requestedCoreUnits, err := requestedAcceleratorVirtualizationCoreUnits(resources)
	if err != nil {
		return endpointAcceleratorVirtualizationCapacityError(err.Error())
	}

	satisfiableDevices := countSatisfiableAcceleratorVirtualizationDevices(resourceInfo, product, requestedMemoryMiB, requestedCoreUnits)
	if satisfiableDevices < requestedGPU {
		return endpointAcceleratorVirtualizationCapacityError(fmt.Sprintf(
			"product=%s requested_gpu=%d requested_memory_mib=%d requested_core_units=%d satisfiable_devices=%d",
			product,
			requestedGPU,
			requestedMemoryMiB,
			requestedCoreUnits,
			satisfiableDevices,
		))
	}

	return nil
}

func clusterResourceInfo(cluster *v1.Cluster) *v1.ClusterResources {
	if cluster == nil || cluster.Status == nil {
		return nil
	}

	return cluster.Status.ResourceInfo
}

func acceleratorVirtualizationProductResource(resourceInfo *v1.ClusterResources, product string) *v1.AcceleratorProductResource {
	if resourceInfo == nil || resourceInfo.Available == nil || resourceInfo.Available.AcceleratorGroups == nil {
		return nil
	}

	group := resourceInfo.Available.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU]
	if group == nil || group.Products == nil {
		return nil
	}

	return group.Products[v1.AcceleratorProduct(product)]
}

func requestedAcceleratorVirtualizationMemoryMiB(resources *v1.ResourceSpec, resourceInfo *v1.ClusterResources, product string) (int64, error) {
	if memoryMiB := resources.GetAcceleratorVirtualizationMemoryMiB(); memoryMiB != "" {
		return parseRequiredPositiveInteger(memoryMiB, "virtualization.memory_mib")
	}

	memoryPercent := resources.GetAcceleratorVirtualizationMemoryPercent()
	if memoryPercent == "" {
		return 0, nil
	}

	percent, err := parseRequiredPositiveInteger(memoryPercent, "virtualization.memory_percent")
	if err != nil {
		return 0, err
	}

	totalMemoryMiB := productTotalMemoryMiB(resourceInfo, product)
	if totalMemoryMiB <= 0 {
		return 0, fmt.Errorf("product=%s missing accelerator memory metadata for virtualization.memory_percent", product)
	}

	return int64(math.Ceil(totalMemoryMiB * float64(percent) / 100)), nil
}

func productTotalMemoryMiB(resourceInfo *v1.ClusterResources, product string) float64 {
	if resourceInfo == nil || resourceInfo.AcceleratorMetadata == nil {
		return 0
	}

	metadata := resourceInfo.AcceleratorMetadata[v1.AcceleratorTypeNVIDIAGPU]
	if metadata == nil || metadata.Products == nil {
		return 0
	}

	productMetadata := metadata.Products[v1.AcceleratorProduct(product)]
	if productMetadata == nil {
		return 0
	}

	return productMetadata.MemoryTotalMiB
}

func requestedAcceleratorVirtualizationCoreUnits(resources *v1.ResourceSpec) (int64, error) {
	corePercent := resources.GetAcceleratorVirtualizationCorePercent()
	if corePercent == "" {
		return 0, nil
	}

	return parseRequiredPositiveInteger(corePercent, "virtualization.core_percent")
}

func countSatisfiableAcceleratorVirtualizationDevices(resourceInfo *v1.ClusterResources, product string, requestedMemoryMiB int64, requestedCoreUnits int64) int64 {
	if resourceInfo == nil {
		return 0
	}

	var count int64

	for _, node := range resourceInfo.NodeResources {
		if node == nil {
			continue
		}

		for _, device := range node.Devices {
			if device == nil || !device.Health || device.Product != product || device.Available == nil {
				continue
			}

			if requestedMemoryMiB > 0 && device.Available.MemoryMiB < requestedMemoryMiB {
				continue
			}

			if requestedCoreUnits > 0 && device.Available.CoreUnits < requestedCoreUnits {
				continue
			}

			count++
		}
	}

	return count
}

func parsePositiveIntegerResource(value *string, field string) (int64, error) {
	if value == nil || *value == "" {
		return 0, fmt.Errorf("%s must be a positive integer", field)
	}

	return parseRequiredPositiveInteger(*value, field)
}

func parseOptionalPositiveInteger(value string, field string) (int64, error) {
	if value == "" {
		return 0, nil
	}

	return parseRequiredPositiveInteger(value, field)
}

func parseRequiredPositiveInteger(value string, field string) (int64, error) {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", field)
	}

	return parsed, nil
}

func validatePercentResource(value string, field string) error {
	if value == "" {
		return nil
	}

	parsed, err := parseOptionalPositiveInteger(value, field)
	if err != nil {
		return err
	}

	if parsed > 100 {
		return fmt.Errorf("%s must be between 1 and 100", field)
	}

	return nil
}

func invalidEndpointPayloadError(err error) *validationError {
	return &validationError{
		Code:    "10214",
		Message: "invalid endpoint payload",
		Hint:    err.Error(),
	}
}

func endpointResourceValueError(err error) *validationError {
	return &validationError{
		Code:    "10216",
		Message: "invalid endpoint accelerator virtualization resources",
		Hint:    err.Error(),
	}
}

func endpointAcceleratorVirtualizationCapacityError(hint string) *validationError {
	return &validationError{
		Code:    "10220",
		Message: "endpoint accelerator virtualization resources exceed cluster availability",
		Hint:    hint,
	}
}
