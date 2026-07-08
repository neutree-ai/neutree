package proxies

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

func validateEndpointVGPU(store storage.Storage) gin.HandlerFunc {
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

		validationErr := validateEndpointVGPURequest(
			store,
			c.Request.Method,
			c.Request.URL.Query(),
			body,
		)
		if validationErr != nil {
			c.JSON(validationErrStatus(validationErr), validationErr)
			c.Abort()

			return
		}

		c.Next()
	}
}

func validateEndpointVGPURequest(
	store storage.Storage,
	method string,
	queryParams url.Values,
	body []byte,
) *validationError {
	endpoint, validationErr := parseEndpointBody(body)
	if validationErr != nil {
		return validationErr
	}

	return validateEndpointVGPUPreflight(store, method, queryParams, endpoint)
}

func parseEndpointBody(body []byte) (*v1.Endpoint, *validationError) {
	var endpoint v1.Endpoint
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&endpoint); err != nil {
		return nil, invalidEndpointPayloadError(err)
	}

	return &endpoint, nil
}

func validateEndpointVGPUPreflight(
	store storage.Storage,
	method string,
	queryParams url.Values,
	endpoint *v1.Endpoint,
) *validationError {
	if endpoint == nil || endpoint.GetDeletionTimestamp() != "" {
		return nil
	}

	if endpoint.Spec == nil {
		return nil
	}

	if method == http.MethodPatch {
		if !endpointPatchMayAffectVGPUValidation(endpoint) {
			return nil
		}
	}

	targetEndpoint, validationErr := endpointVGPUValidationTarget(store, method, queryParams, endpoint)
	if validationErr != nil {
		return validationErr
	}

	if targetEndpoint == nil || targetEndpoint.Spec == nil {
		return nil
	}

	if validationErr := validateEndpointReplicaCount(targetEndpoint.Spec); validationErr != nil {
		return validationErr
	}

	if targetEndpoint.Spec.Resources == nil || !targetEndpoint.Spec.Resources.HasAcceleratorVirtualization() {
		return nil
	}

	if endpointReplicaCount(targetEndpoint.Spec) == 0 {
		return nil
	}

	cluster, validationErr := resolveEndpointVGPUCluster(store, targetEndpoint)
	if validationErr != nil {
		return validationErr
	}

	if validationErr := validateEndpointVGPUCluster(cluster); validationErr != nil {
		return validationErr
	}

	if validationErr := validateEndpointVGPUResourceShape(targetEndpoint.Spec.Resources); validationErr != nil {
		return validationErr
	}

	if validationErr := validateEndpointVGPUMemorySpec(targetEndpoint.Spec.Resources, cluster); validationErr != nil {
		return validationErr
	}

	// Capacity is intentionally left to scheduling/runtime status. Cluster
	// resource snapshots can be stale and should not be used as admission gates.
	return nil
}

func endpointVGPUValidationTarget(
	store storage.Storage,
	method string,
	queryParams url.Values,
	endpoint *v1.Endpoint,
) (*v1.Endpoint, *validationError) {
	if method != http.MethodPatch {
		return endpoint, nil
	}

	resolved, validationErr := resolveEndpointPatch(store, queryParams)
	if validationErr != nil {
		return nil, validationErr
	}

	merged := mergeEndpointPatch(resolved, endpoint)

	return &merged, nil
}

func resolveEndpointPatch(
	store storage.Storage,
	queryParams url.Values,
) (*v1.Endpoint, *validationError) {
	if store == nil {
		return nil, endpointVGPULookupError("storage is required to validate endpoint accelerator virtualization")
	}

	filters := queryParamsToFilters(queryParams)
	if len(filters) == 0 {
		return nil, endpointVGPUTargetError("endpoint lookup filters are required for vGPU resource PATCH")
	}

	endpoints, err := store.ListEndpoint(storage.ListOption{Filters: filters})
	if err != nil {
		return nil, endpointVGPULookupError("failed to look up endpoint for vGPU resource PATCH")
	}

	if len(endpoints) == 0 {
		return nil, endpointVGPUTargetError("endpoint not found for vGPU resource PATCH")
	}

	if len(endpoints) > 1 {
		return nil, endpointVGPUTargetError("multiple endpoints matched vGPU resource PATCH filters")
	}

	return &endpoints[0], nil
}

func resolveEndpointVGPUCluster(store storage.Storage, endpoint *v1.Endpoint) (*v1.Cluster, *validationError) {
	if store == nil {
		return nil, endpointVGPULookupError("storage is required to validate endpoint accelerator virtualization")
	}

	clusterName := endpoint.Spec.Cluster
	if clusterName == "" {
		return nil, endpointVGPUTargetError("spec.cluster is required for endpoint accelerator virtualization")
	}

	workspace := defaultWorkspace
	if endpoint.Metadata != nil && endpoint.Metadata.Workspace != "" {
		workspace = endpoint.Metadata.Workspace
	}

	clusters, err := store.ListCluster(storage.ListOption{
		Filters: endpointClusterLookupFilters(clusterName, workspace),
	})
	if err != nil {
		return nil, endpointVGPULookupError("failed to look up cluster for endpoint accelerator virtualization")
	}

	if len(clusters) == 0 {
		return nil, endpointVGPUTargetError(fmt.Sprintf("cluster %s/%s not found", workspace, clusterName))
	}

	if len(clusters) > 1 {
		return nil, endpointVGPUTargetError(fmt.Sprintf("multiple clusters matched %s/%s", workspace, clusterName))
	}

	return &clusters[0], nil
}

func mergeEndpointPatch(existing *v1.Endpoint, patch *v1.Endpoint) v1.Endpoint {
	if existing == nil {
		if patch == nil {
			return v1.Endpoint{}
		}

		return *patch
	}

	merged := *existing

	if existing.Metadata != nil {
		metadata := *existing.Metadata
		merged.Metadata = &metadata
	}

	if existing.Spec != nil {
		spec := *existing.Spec
		if existing.Spec.Resources != nil {
			spec.Resources = copyEndpointResourceSpec(existing.Spec.Resources)
		}

		merged.Spec = &spec
	}

	if patch == nil {
		return merged
	}

	if patch.Metadata != nil {
		if merged.Metadata == nil {
			merged.Metadata = &v1.Metadata{}
		}

		if patch.Metadata.Name != "" {
			merged.Metadata.Name = patch.Metadata.Name
		}

		if patch.Metadata.Workspace != "" {
			merged.Metadata.Workspace = patch.Metadata.Workspace
		}
	}

	if patch.Spec != nil {
		if merged.Spec == nil {
			merged.Spec = &v1.EndpointSpec{}
		}

		if patch.Spec.Cluster != "" {
			merged.Spec.Cluster = patch.Spec.Cluster
		}

		if patch.Spec.Replicas.Num != nil {
			merged.Spec.Replicas.Num = patch.Spec.Replicas.Num
		}

		if patch.Spec.Resources != nil {
			merged.Spec.Resources = mergeEndpointResourceSpec(merged.Spec.Resources, patch.Spec.Resources)
		}
	}

	return merged
}

func mergeEndpointResourceSpec(existing *v1.ResourceSpec, patch *v1.ResourceSpec) *v1.ResourceSpec {
	if existing == nil {
		return copyEndpointResourceSpec(patch)
	}

	merged := copyEndpointResourceSpec(existing)

	if patch.CPU != nil {
		merged.CPU = patch.CPU
	}

	if patch.GPU != nil {
		merged.GPU = patch.GPU
	}

	if patch.Memory != nil {
		merged.Memory = patch.Memory
	}

	if patch.Accelerator != nil {
		if merged.Accelerator == nil {
			merged.Accelerator = make(map[string]string, len(patch.Accelerator))
		}

		for key, value := range patch.Accelerator {
			merged.Accelerator[key] = value
		}

		if acceleratorPatchClearsVirtualization(patch.Accelerator) {
			clearAcceleratorVirtualization(merged.Accelerator)
		}
	}

	return merged
}

func acceleratorPatchClearsVirtualization(accelerator map[string]string) bool {
	if accelerator == nil {
		return false
	}

	_, hasType := accelerator[v1.AcceleratorTypeKey]
	_, hasProduct := accelerator[v1.AcceleratorProductKey]

	return hasType && hasProduct && !acceleratorHasVirtualization(accelerator)
}

func clearAcceleratorVirtualization(accelerator map[string]string) {
	delete(accelerator, v1.AcceleratorVirtualizationMemoryMiBKey)
	delete(accelerator, v1.AcceleratorVirtualizationMemoryPercentKey)
	delete(accelerator, v1.AcceleratorVirtualizationCorePercentKey)
}

func acceleratorHasVirtualization(accelerator map[string]string) bool {
	for key := range accelerator {
		if v1.IsAcceleratorVirtualizationKey(key) {
			return true
		}
	}

	return false
}

func copyEndpointResourceSpec(resources *v1.ResourceSpec) *v1.ResourceSpec {
	if resources == nil {
		return nil
	}

	copied := *resources
	if resources.Accelerator != nil {
		copied.Accelerator = make(map[string]string, len(resources.Accelerator))
		for key, value := range resources.Accelerator {
			copied.Accelerator[key] = value
		}
	}

	return &copied
}

func endpointPatchMayAffectVGPUValidation(endpoint *v1.Endpoint) bool {
	if endpoint == nil || endpoint.Spec == nil {
		return false
	}

	return endpoint.Spec.Resources != nil ||
		endpoint.Spec.Cluster != "" ||
		endpoint.Spec.Replicas.Num != nil
}

func endpointClusterLookupFilters(cluster, workspace string) []storage.Filter {
	return []storage.Filter{
		{Column: "metadata->name", Operator: "eq", Value: strconv.Quote(cluster)},
		{Column: "metadata->workspace", Operator: "eq", Value: strconv.Quote(workspace)},
	}
}

func validateEndpointVGPUCluster(cluster *v1.Cluster) *validationError {
	if cluster == nil || cluster.Spec == nil || !cluster.Spec.AcceleratorVirtualizationEnabled() {
		return endpointVGPUNotReadyError(cluster, "cluster accelerator virtualization is not enabled")
	}

	if cluster.Spec.Type != v1.KubernetesClusterType {
		return endpointVGPUNotReadyError(cluster, "endpoint accelerator virtualization is only supported for kubernetes clusters")
	}

	if cluster.Status == nil || cluster.Status.ComponentStatus == nil {
		return endpointVGPUNotReadyError(cluster, "cluster accelerator virtualization component status is missing")
	}

	component := cluster.Status.ComponentStatus[v1.ComponentStatusAcceleratorVirtualizationKey]
	if component == nil {
		return endpointVGPUNotReadyError(cluster, "cluster accelerator virtualization component status is missing")
	}

	if component.Phase != v1.ComponentPhaseReady {
		hint := "cluster accelerator virtualization is not ready"
		if component.Reason != "" || component.Message != "" {
			hint = fmt.Sprintf("%s: %s %s", hint, component.Reason, component.Message)
		}

		return endpointVGPUNotReadyError(cluster, hint)
	}

	return nil
}

func validateEndpointVGPUResourceShape(resources *v1.ResourceSpec) *validationError {
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

	if resources.GetAcceleratorVirtualizationMemoryPercent() != "" {
		return &validationError{
			Code:    "10219",
			Message: "virtualization memory_percent is not supported",
			Hint:    "Set virtualization.memory_mib instead of virtualization.memory_percent",
		}
	}

	if _, err := parsePositiveIntegerResource(resources.GPU, "spec.resources.gpu"); err != nil {
		return endpointResourceValueError(err)
	}

	if _, err := parseRequiredPositiveInteger(resources.GetAcceleratorVirtualizationMemoryMiB(), "virtualization.memory_mib"); err != nil {
		return endpointResourceValueError(err)
	}

	if err := validateZeroToHundredPercentResource(resources.GetAcceleratorVirtualizationCorePercent(), "virtualization.core_percent"); err != nil {
		return endpointResourceValueError(err)
	}

	return nil
}

func validateEndpointVGPUMemorySpec(resources *v1.ResourceSpec, cluster *v1.Cluster) *validationError {
	if resources == nil || !resources.HasAcceleratorVirtualization() {
		return nil
	}

	requestedMemoryMiB, err := parseRequiredPositiveInteger(
		resources.GetAcceleratorVirtualizationMemoryMiB(),
		"virtualization.memory_mib",
	)
	if err != nil {
		return endpointResourceValueError(err)
	}

	product := resources.GetAcceleratorProduct()
	maxMemoryMiB, ok := clusterProductMaxMemoryMiB(cluster, product)

	if !ok {
		return endpointResourceValueError(fmt.Errorf(
			"unable to determine physical GPU memory_mib for accelerator product %s",
			product,
		))
	}

	if requestedMemoryMiB > maxMemoryMiB {
		return endpointResourceValueError(fmt.Errorf(
			"virtualization.memory_mib must be less than or equal to physical GPU memory_mib %d for accelerator product %s",
			maxMemoryMiB,
			product,
		))
	}

	return nil
}

func clusterProductMaxMemoryMiB(cluster *v1.Cluster, product string) (int64, bool) {
	resourceInfo := clusterResourceInfo(cluster)
	if resourceInfo == nil {
		return 0, false
	}

	if memoryMiB, ok := clusterProductMetadataMemoryMiB(resourceInfo, product); ok {
		return memoryMiB, true
	}

	var maxMemoryMiB int64

	for _, node := range resourceInfo.NodeResources {
		if node == nil {
			continue
		}

		for _, device := range node.Devices {
			if device == nil || device.Product != product || device.Allocatable == nil {
				continue
			}

			if device.Allocatable.MemoryMiB > maxMemoryMiB {
				maxMemoryMiB = device.Allocatable.MemoryMiB
			}
		}
	}

	return maxMemoryMiB, maxMemoryMiB > 0
}

func clusterProductMetadataMemoryMiB(resourceInfo *v1.ClusterResources, product string) (int64, bool) {
	if resourceInfo.AcceleratorMetadata == nil {
		return 0, false
	}

	metadata := resourceInfo.AcceleratorMetadata[v1.AcceleratorTypeNVIDIAGPU]
	if metadata == nil || metadata.Products == nil {
		return 0, false
	}

	productMetadata := metadata.Products[v1.AcceleratorProduct(product)]
	if productMetadata == nil || productMetadata.MemoryTotalMiB <= 0 {
		return 0, false
	}

	return int64(productMetadata.MemoryTotalMiB), true
}

func endpointReplicaCount(spec *v1.EndpointSpec) int64 {
	if spec == nil || spec.Replicas.Num == nil {
		return 1
	}

	return int64(*spec.Replicas.Num)
}

func validateEndpointReplicaCount(spec *v1.EndpointSpec) *validationError {
	if spec == nil || spec.Replicas.Num == nil {
		return nil
	}

	if *spec.Replicas.Num < 0 {
		return endpointResourceValueError(fmt.Errorf("spec.replicas.num must be a non-negative integer"))
	}

	return nil
}

func clusterResourceInfo(cluster *v1.Cluster) *v1.ClusterResources {
	if cluster == nil || cluster.Status == nil {
		return nil
	}

	return cluster.Status.ResourceInfo
}

func parsePositiveIntegerResource(value *string, field string) (int64, error) {
	if value == nil || *value == "" {
		return 0, fmt.Errorf("%s must be a positive integer", field)
	}

	return parseRequiredPositiveInteger(*value, field)
}

func parseRequiredPositiveInteger(value string, field string) (int64, error) {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", field)
	}

	return parsed, nil
}

func validateZeroToHundredPercentResource(value string, field string) error {
	if value == "" {
		return nil
	}

	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 || parsed > 100 {
		return fmt.Errorf("%s must be between 0 and 100", field)
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

func endpointVGPUTargetError(hint string) *validationError {
	return &validationError{
		Code:    "10221",
		Message: "invalid endpoint accelerator virtualization target",
		Hint:    hint,
	}
}

func endpointVGPULookupError(hint string) *validationError {
	err := endpointVGPUTargetError(hint)
	err.HTTPStatus = http.StatusServiceUnavailable

	return err
}

func validationErrStatus(err *validationError) int {
	if err != nil && err.HTTPStatus != 0 {
		return err.HTTPStatus
	}

	return http.StatusBadRequest
}

func endpointVGPUNotReadyError(cluster *v1.Cluster, hint string) *validationError {
	if cluster != nil && cluster.Metadata != nil {
		hint = fmt.Sprintf("cluster %s/%s accelerator virtualization is not ready: %s", cluster.Metadata.Workspace, cluster.Metadata.Name, hint)
	}

	return &validationError{
		Code:    "10222",
		Message: "cluster accelerator virtualization is not ready",
		Hint:    hint,
	}
}
