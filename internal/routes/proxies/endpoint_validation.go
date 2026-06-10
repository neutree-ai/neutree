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

func validateEndpointAcceleratorVirtualization(s storage.Storage) gin.HandlerFunc {
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

		if validationErr := validateEndpointAcceleratorVirtualizationBody(c.Request.Method, body, c.Request.URL.RawQuery, s); validationErr != nil {
			c.JSON(http.StatusBadRequest, validationErr)
			c.Abort()

			return
		}

		c.Next()
	}
}

func validateEndpointAcceleratorVirtualizationBody(
	method string,
	body []byte,
	rawQuery string,
	s storage.Storage,
) *validationError {
	hasResources, err := endpointPayloadHasResources(body)
	if err != nil {
		return invalidEndpointPayloadError(err)
	}

	if !hasResources {
		return nil
	}

	var endpoint v1.Endpoint
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&endpoint); err != nil {
		return invalidEndpointPayloadError(err)
	}

	if endpoint.Spec == nil || endpoint.Spec.Resources == nil {
		return nil
	}

	resources := endpoint.Spec.Resources
	if err := validateEndpointAcceleratorVirtualizationResourceShape(resources); err != nil {
		return err
	}

	if !resources.HasAcceleratorVirtualization() {
		return nil
	}

	var existing *v1.Endpoint

	if method == http.MethodPatch {
		loadedEndpoint, err := existingEndpointForPatch(rawQuery, s)
		if err != nil {
			// PATCH can still be validated without loading the existing row when
			// the payload carries enough context for cluster resource lookup.
			if endpoint.Spec.Cluster == "" || endpoint.GetWorkspace() == "" {
				return &validationError{
					Code:    "10215",
					Message: "failed to load existing endpoint for resource validation",
					Hint:    err.Error(),
				}
			}
		} else {
			existing = loadedEndpoint
			endpoint = mergeEndpointForResourceValidation(existing, endpoint)
		}
	}

	if err := validateEndpointAcceleratorVirtualizationResourcePool(&endpoint, existing, s); err != nil {
		return err
	}

	return nil
}

func endpointPayloadHasResources(body []byte) (bool, error) {
	// PATCH is partial. Only requests that explicitly touch spec.resources
	// should trigger accelerator virtualization validation.
	var payload map[string]interface{}
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&payload); err != nil {
		return false, err
	}

	rawSpec, ok := payload["spec"]
	if !ok || rawSpec == nil {
		return false, nil
	}

	spec, ok := rawSpec.(map[string]interface{})
	if !ok {
		return false, fmt.Errorf("spec must be an object")
	}

	_, ok = spec["resources"]

	return ok, nil
}

func validateEndpointAcceleratorVirtualizationResourceShape(resources *v1.ResourceSpec) *validationError {
	for key := range resources.Accelerator {
		switch key {
		case "nvidia.com/gpumem", "nvidia.com/gpumem-percentage", "nvidia.com/gpucores":
			return &validationError{
				Code:    "10216",
				Message: "raw HAMi resource keys are not supported in endpoint accelerator resources",
				Hint:    "Use accelerator virtualization fields instead of nvidia.com/gpumem, nvidia.com/gpumem-percentage, or nvidia.com/gpucores",
			}
		}
	}

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

	if _, err := parsePositiveFloat(resources.GetAcceleratorVirtualizationMemoryMiB(), "virtualization.memory_mib"); err != nil {
		return endpointResourceValueError(err)
	}

	if _, err := parsePercent(resources.GetAcceleratorVirtualizationMemoryPercent(), "virtualization.memory_percent"); err != nil {
		return endpointResourceValueError(err)
	}

	if _, err := parsePercent(resources.GetAcceleratorVirtualizationCorePercent(), "virtualization.core_percent"); err != nil {
		return endpointResourceValueError(err)
	}

	return nil
}

func validateEndpointAcceleratorVirtualizationResourcePool(endpoint *v1.Endpoint, existing *v1.Endpoint, s storage.Storage) *validationError {
	if endpoint == nil || endpoint.Spec == nil || endpoint.Spec.Resources == nil {
		return nil
	}

	if endpoint.Spec.Cluster == "" {
		return &validationError{
			Code:    "10220",
			Message: "endpoint accelerator virtualization requires deploy cluster",
			Hint:    "Set spec.cluster before configuring accelerator virtualization resources",
		}
	}

	if s == nil {
		return &validationError{
			Code:    "10215",
			Message: "failed to load endpoint deploy cluster for resource validation",
			Hint:    "storage is not configured",
		}
	}

	cluster, err := endpointDeployClusterForValidation(endpoint, s)
	if err != nil {
		return &validationError{
			Code:    "10215",
			Message: "failed to load endpoint deploy cluster for resource validation",
			Hint:    err.Error(),
		}
	}

	resources := endpoint.Spec.Resources
	gpuCount, err := parsePositiveIntegerResource(resources.GPU, "spec.resources.gpu")

	if err != nil {
		return endpointResourceValueError(err)
	}

	productName := resources.GetAcceleratorProduct()
	availableProduct, ok := getAvailableVirtualizationProduct(cluster.Status, resources.GetAcceleratorType(), productName)

	if !ok {
		return &validationError{
			Code:    "10221",
			Message: fmt.Sprintf("endpoint requires vGPU product %s, but it is not available", productName),
			Hint:    "Refresh cluster resources or select an available accelerator product",
		}
	}

	reclaimedQuantity, reclaimedMemoryMiB, reclaimedCoreUnits := existingEndpointVirtualizationAllocation(existing, productName)
	availableQuantity := availableProduct.Quantity + reclaimedQuantity

	if float64(gpuCount) > availableQuantity {
		return &validationError{
			Code:    "10222",
			Message: fmt.Sprintf("endpoint requires %d %s vGPU device(s), but available vGPU product quantity is %.0f", gpuCount, productName, availableQuantity),
			Hint:    "Reduce spec.resources.gpu or select a product with enough available GPU devices",
		}
	}

	requiredMemoryMiB, err := getRequiredVirtualizationMemoryMiB(resources, cluster.Status)
	if err != nil {
		return endpointResourceValueError(err)
	}

	if requiredMemoryMiB > 0 {
		availableMemoryMiB := float64(0)
		if availableProduct.Virtualization != nil {
			availableMemoryMiB = availableProduct.Virtualization.MemoryMiB
		}

		availableMemoryMiB += reclaimedMemoryMiB
		totalRequiredMemoryMiB := float64(gpuCount) * requiredMemoryMiB

		if totalRequiredMemoryMiB > availableMemoryMiB {
			return &validationError{
				Code:    "10223",
				Message: fmt.Sprintf("endpoint requires %.0f MiB vGPU memory, but available vGPU memory for %s is %.0f MiB", totalRequiredMemoryMiB, productName, availableMemoryMiB),
				Hint:    "Reduce virtualization.memory_mib, virtualization.memory_percent, or spec.resources.gpu",
			}
		}
	}

	requiredCoreUnits, err := getRequiredVirtualizationCoreUnits(resources)
	if err != nil {
		return endpointResourceValueError(err)
	}

	if requiredCoreUnits > 0 {
		availableCoreUnits := float64(0)
		if availableProduct.Virtualization != nil {
			availableCoreUnits = availableProduct.Virtualization.CoreUnits
		}

		availableCoreUnits += reclaimedCoreUnits
		totalRequiredCoreUnits := float64(gpuCount) * requiredCoreUnits

		if totalRequiredCoreUnits > availableCoreUnits {
			return &validationError{
				Code:    "10224",
				Message: fmt.Sprintf("endpoint requires %.0f vGPU core units, but available vGPU core units for %s is %.0f", totalRequiredCoreUnits, productName, availableCoreUnits),
				Hint:    "Reduce virtualization.core_percent or spec.resources.gpu",
			}
		}
	}

	if err := validateEndpointVirtualizationDeviceFit(
		cluster.Status,
		existing,
		productName,
		gpuCount,
		requiredMemoryMiB,
		requiredCoreUnits,
	); err != nil {
		return err
	}

	return nil
}

func endpointDeployClusterForValidation(endpoint *v1.Endpoint, s storage.Storage) (*v1.Cluster, error) {
	filters := []storage.Filter{
		{Column: "metadata->name", Operator: "eq", Value: strconv.Quote(endpoint.Spec.Cluster)},
	}
	if endpoint.GetWorkspace() != "" {
		filters = append(filters, storage.Filter{
			Column:   "metadata->workspace",
			Operator: "eq",
			Value:    strconv.Quote(endpoint.GetWorkspace()),
		})
	}

	clusters, err := s.ListCluster(storage.ListOption{Filters: filters})
	if err != nil {
		return nil, fmt.Errorf("failed to list cluster: %w", err)
	}

	if len(clusters) == 0 {
		return nil, fmt.Errorf("cluster %s not found", endpoint.Spec.Cluster)
	}

	return &clusters[0], nil
}

func existingEndpointForPatch(rawQuery string, s storage.Storage) (*v1.Endpoint, error) {
	if s == nil {
		return nil, fmt.Errorf("include spec.cluster and metadata.workspace when configuring accelerator virtualization")
	}

	filters := filtersFromRawQuery(rawQuery)
	if len(filters) == 0 {
		return nil, fmt.Errorf("endpoint patch must include filters or spec.cluster and metadata.workspace")
	}

	endpoints, err := s.ListEndpoint(storage.ListOption{Filters: filters})
	if err != nil {
		return nil, fmt.Errorf("failed to load existing endpoint: %w", err)
	}

	if len(endpoints) != 1 {
		return nil, fmt.Errorf("endpoint patch must match exactly one existing endpoint")
	}

	return &endpoints[0], nil
}

func mergeEndpointForResourceValidation(existing *v1.Endpoint, patch v1.Endpoint) v1.Endpoint {
	merged := v1.Endpoint{
		ID:         patch.ID,
		APIVersion: patch.APIVersion,
		Kind:       patch.Kind,
		Metadata:   patch.Metadata,
		Spec:       patch.Spec,
		Status:     patch.Status,
	}

	if existing == nil {
		return merged
	}

	if merged.Metadata == nil {
		merged.Metadata = existing.Metadata
	} else if existing.Metadata != nil {
		metadata := *merged.Metadata
		if metadata.Name == "" {
			metadata.Name = existing.Metadata.Name
		}

		if metadata.Workspace == "" {
			metadata.Workspace = existing.Metadata.Workspace
		}

		merged.Metadata = &metadata
	}

	if merged.Spec == nil {
		merged.Spec = existing.Spec
	} else if existing.Spec != nil {
		spec := *merged.Spec
		if spec.Cluster == "" {
			spec.Cluster = existing.Spec.Cluster
		}

		merged.Spec = &spec
	}

	return merged
}

func existingEndpointVirtualizationAllocation(endpoint *v1.Endpoint, productName string) (float64, float64, float64) {
	if endpoint == nil ||
		endpoint.Status == nil ||
		endpoint.Status.Resources == nil {
		return 0, 0, 0
	}

	var quantity float64
	var memoryMiB float64
	var coreUnits float64

	for _, replica := range endpoint.Status.Resources.Replicas {
		for _, device := range replica.Devices {
			if device.Product != productName {
				continue
			}

			quantity++
			memoryMiB += float64(device.MemoryMiB)
			coreUnits += float64(device.CoreUnits)
		}
	}

	if quantity > 0 {
		return quantity, memoryMiB, coreUnits
	}

	// Older status records may only have summary-level usage. Quantity cannot be
	// safely reclaimed without per-replica devices, but memory/core can still be
	// excluded from the current endpoint's own update check.
	if endpoint.Status.Resources.Summary == nil ||
		endpoint.Status.Resources.Summary.Products == nil {
		return 0, 0, 0
	}

	usage := endpoint.Status.Resources.Summary.Products[v1.AcceleratorProduct(productName)]
	if usage == nil {
		return 0, 0, 0
	}

	return 0, float64(usage.MemoryMiB), float64(usage.CoreUnits)
}

type endpointVirtualizationDeviceCapacity struct {
	uuid      string
	memoryMiB int64
	coreUnits int64
}

func validateEndpointVirtualizationDeviceFit(
	status *v1.ClusterStatus,
	existing *v1.Endpoint,
	productName string,
	gpuCount int64,
	requiredMemoryMiB float64,
	requiredCoreUnits float64,
) *validationError {
	devices := availableEndpointVirtualizationDevices(status, productName)
	if len(devices) == 0 {
		return nil
	}

	reclaims := existingEndpointVirtualizationDeviceReclaims(existing, productName)
	for i, device := range devices {
		reclaim := reclaims[device.uuid]
		// When updating an Endpoint, its current allocation should be available
		// to itself. Add those device-local resources back before fit checks.
		devices[i].memoryMiB += reclaim.memoryMiB
		devices[i].coreUnits += reclaim.coreUnits
	}

	requiredCount := int(gpuCount)
	compatibleCount := 0
	memoryFitCount := 0
	coreFitCount := 0

	for _, device := range devices {
		memoryFits := requiredMemoryMiB <= 0 || float64(device.memoryMiB) >= requiredMemoryMiB
		coreFits := requiredCoreUnits <= 0 || float64(device.coreUnits) >= requiredCoreUnits

		if memoryFits {
			memoryFitCount++
		}

		if coreFits {
			coreFitCount++
		}

		if memoryFits && coreFits {
			compatibleCount++
		}
	}

	if compatibleCount >= requiredCount {
		return nil
	}

	if requiredMemoryMiB > 0 && memoryFitCount < requiredCount {
		return &validationError{
			Code: "10223",
			Message: fmt.Sprintf("endpoint requires %.0f MiB vGPU memory per device, but only %d %s device(s) can satisfy it",
				requiredMemoryMiB, memoryFitCount, productName),
			Hint: "Reduce virtualization.memory_mib, virtualization.memory_percent, or spec.resources.gpu",
		}
	}

	if requiredCoreUnits > 0 && coreFitCount < requiredCount {
		return &validationError{
			Code: "10224",
			Message: fmt.Sprintf("endpoint requires %.0f vGPU core units per device, but only %d %s device(s) can satisfy it",
				requiredCoreUnits, coreFitCount, productName),
			Hint: "Reduce virtualization.core_percent or spec.resources.gpu",
		}
	}

	return &validationError{
		Code: "10222",
		Message: fmt.Sprintf("endpoint requires %d %s vGPU device(s) satisfying memory and core requirements, but available compatible device quantity is %d",
			gpuCount, productName, compatibleCount),
		Hint: "Reduce virtualization.memory_mib, virtualization.memory_percent, virtualization.core_percent, or spec.resources.gpu",
	}
}

func availableEndpointVirtualizationDevices(status *v1.ClusterStatus, productName string) []endpointVirtualizationDeviceCapacity {
	if status == nil ||
		status.ResourceInfo == nil ||
		status.ResourceInfo.NodeResources == nil {
		return nil
	}

	devices := []endpointVirtualizationDeviceCapacity{}

	for _, node := range status.ResourceInfo.NodeResources {
		if node == nil {
			continue
		}

		for _, device := range node.Devices {
			if device == nil ||
				!device.Health ||
				device.Product != productName ||
				device.Available == nil {
				continue
			}

			devices = append(devices, endpointVirtualizationDeviceCapacity{
				uuid:      device.UUID,
				memoryMiB: device.Available.MemoryMiB,
				coreUnits: device.Available.CoreUnits,
			})
		}
	}

	return devices
}

func existingEndpointVirtualizationDeviceReclaims(
	endpoint *v1.Endpoint,
	productName string,
) map[string]endpointVirtualizationDeviceCapacity {
	result := make(map[string]endpointVirtualizationDeviceCapacity)
	if endpoint == nil ||
		endpoint.Status == nil ||
		endpoint.Status.Resources == nil {
		return result
	}

	for _, replica := range endpoint.Status.Resources.Replicas {
		for _, device := range replica.Devices {
			if device.UUID == "" || device.Product != productName {
				continue
			}

			reclaim := result[device.UUID]
			reclaim.uuid = device.UUID
			reclaim.memoryMiB += device.MemoryMiB
			reclaim.coreUnits += device.CoreUnits
			result[device.UUID] = reclaim
		}
	}

	return result
}

func getAvailableVirtualizationProduct(status *v1.ClusterStatus, acceleratorType string, productName string) (*v1.AcceleratorProductResource, bool) {
	if status == nil ||
		status.ResourceInfo == nil ||
		status.ResourceInfo.Available == nil ||
		status.ResourceInfo.Available.AcceleratorGroups == nil {
		return nil, false
	}

	group := status.ResourceInfo.Available.AcceleratorGroups[v1.AcceleratorType(acceleratorType)]
	if group == nil || group.Products == nil {
		return nil, false
	}

	product := group.Products[v1.AcceleratorProduct(productName)]
	if product == nil {
		return nil, false
	}

	return product, true
}

func getRequiredVirtualizationMemoryMiB(resources *v1.ResourceSpec, status *v1.ClusterStatus) (float64, error) {
	if resources.GetAcceleratorVirtualizationMemoryMiB() != "" {
		return parsePositiveFloat(resources.GetAcceleratorVirtualizationMemoryMiB(), "virtualization.memory_mib")
	}

	memoryPercent, err := parsePercent(resources.GetAcceleratorVirtualizationMemoryPercent(), "virtualization.memory_percent")
	if err != nil || memoryPercent == 0 {
		return 0, err
	}

	productMetadata, ok := getAcceleratorProductMetadata(status, resources.GetAcceleratorType(), resources.GetAcceleratorProduct())
	if !ok || productMetadata.MemoryTotalMiB <= 0 {
		return 0, fmt.Errorf("accelerator product %s memory metadata is not available", resources.GetAcceleratorProduct())
	}

	// Round up so percentage-based requests never under-reserve memory after
	// converting from product total memory to MiB.
	return math.Ceil(productMetadata.MemoryTotalMiB * memoryPercent / 100), nil
}

func getRequiredVirtualizationCoreUnits(resources *v1.ResourceSpec) (float64, error) {
	return parsePercent(resources.GetAcceleratorVirtualizationCorePercent(), "virtualization.core_percent")
}

func getAcceleratorProductMetadata(status *v1.ClusterStatus, acceleratorType string, productName string) (*v1.AcceleratorProductMetadata, bool) {
	if status == nil ||
		status.ResourceInfo == nil ||
		status.ResourceInfo.AcceleratorMetadata == nil {
		return nil, false
	}

	metadata := status.ResourceInfo.AcceleratorMetadata[v1.AcceleratorType(acceleratorType)]
	if metadata == nil || metadata.Products == nil {
		return nil, false
	}

	productMetadata := metadata.Products[v1.AcceleratorProduct(productName)]
	if productMetadata == nil {
		return nil, false
	}

	return productMetadata, true
}

func parsePositiveIntegerResource(value *string, field string) (int64, error) {
	if value == nil || *value == "" {
		return 0, fmt.Errorf("%s must be a positive integer", field)
	}

	parsed, err := strconv.ParseInt(*value, 10, 64)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", field)
	}

	return parsed, nil
}

func parsePositiveFloat(value string, field string) (float64, error) {
	if value == "" {
		return 0, nil
	}

	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive number", field)
	}

	return parsed, nil
}

func parsePercent(value string, field string) (float64, error) {
	if value == "" {
		return 0, nil
	}

	parsed, err := parsePositiveFloat(value, field)
	if err != nil {
		return 0, err
	}

	if parsed > 100 {
		return 0, fmt.Errorf("%s must be between 1 and 100", field)
	}

	return parsed, nil
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
