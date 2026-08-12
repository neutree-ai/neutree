package proxies

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strconv"

	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type endpointValidationOperation string

const (
	endpointValidationCreate     endpointValidationOperation = "create"
	endpointValidationPatch      endpointValidationOperation = "patch"
	endpointValidationSoftDelete endpointValidationOperation = "soft-delete"
)

type endpointValidationInput struct {
	Method      string
	Body        []byte
	RawPayload  map[string]json.RawMessage
	Patch       v1.Endpoint
	Current     *v1.Endpoint
	New         *v1.Endpoint
	QueryParams url.Values
	Operation   endpointValidationOperation
}

type endpointValidator func(storage.Storage, *endpointValidationInput) *validationError

type endpointValidationConfig struct {
	Validators []endpointValidator
}

var endpointValidationConfigs = map[endpointValidationOperation]endpointValidationConfig{
	endpointValidationCreate: {
		Validators: []endpointValidator{
			validateEndpointCreateVGPU,
		},
	},
	endpointValidationPatch: {
		Validators: []endpointValidator{
			validateEndpointPatchClusterImmutable,
			validateEndpointPatchVGPU,
		},
	},
	endpointValidationSoftDelete: {},
}

func validateEndpoint(store storage.Storage) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method != http.MethodPost && c.Request.Method != http.MethodPatch {
			c.Next()
			return
		}

		input, validationErr := readEndpointValidationInput(c)
		if validationErr != nil {
			c.JSON(validationErrStatus(validationErr), validationErr)
			c.Abort()

			return
		}

		config := endpointValidationConfigs[input.Operation]

		if validationErr := prepareEndpointValidationInput(store, input); validationErr != nil {
			c.JSON(validationErrStatus(validationErr), validationErr)
			c.Abort()

			return
		}

		for _, validator := range config.Validators {
			if validationErr := validator(store, input); validationErr != nil {
				c.JSON(validationErrStatus(validationErr), validationErr)
				c.Abort()

				return
			}
		}

		c.Next()
	}
}

func endpointValidationOperationFor(method string) endpointValidationOperation {
	if method == http.MethodPatch {
		return endpointValidationPatch
	}

	return endpointValidationCreate
}

func readEndpointValidationInput(c *gin.Context) (*endpointValidationInput, *validationError) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return nil, invalidEndpointPayloadError(err)
	}

	if err := c.Request.Body.Close(); err != nil {
		return nil, invalidEndpointPayloadError(err)
	}

	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	c.Request.ContentLength = int64(len(body))
	c.Request.Header.Set("Content-Length", strconv.Itoa(len(body)))

	input := &endpointValidationInput{
		Method:      c.Request.Method,
		Body:        body,
		QueryParams: c.Request.URL.Query(),
		Operation:   endpointValidationOperationFor(c.Request.Method),
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return input, nil
	}

	if err := json.Unmarshal(body, &input.RawPayload); err != nil {
		return nil, invalidEndpointPayloadError(err)
	}

	if input.Operation == endpointValidationPatch {
		isSoftDelete, err := endpointPatchIsSoftDelete(input.RawPayload)
		if err != nil {
			return nil, invalidEndpointPayloadError(err)
		}

		if isSoftDelete {
			input.Operation = endpointValidationSoftDelete
			if metadataRaw, ok := input.RawPayload["metadata"]; ok {
				if err := json.Unmarshal(metadataRaw, &input.Patch.Metadata); err != nil {
					return nil, invalidEndpointPayloadError(err)
				}
			}

			return input, nil
		}
	}

	patch, validationErr := parseEndpointBody(body)
	if validationErr != nil {
		return nil, validationErr
	}

	input.Patch = *patch

	return input, nil
}

func prepareEndpointValidationInput(
	store storage.Storage,
	input *endpointValidationInput,
) *validationError {
	if input.Operation == endpointValidationSoftDelete {
		return nil
	}

	input.New = &input.Patch
	if input.Operation != endpointValidationPatch {
		return nil
	}

	current, validationErr := resolveEndpointPatch(store, input.QueryParams)
	if validationErr != nil {
		return validationErr
	}

	newEndpoint, err := buildPostgrestEndpointPatchValidationNew(current, input.Body)
	if err != nil {
		return invalidEndpointPayloadError(err)
	}

	input.Current = current
	input.New = newEndpoint

	return nil
}

func endpointPatchIsSoftDelete(payload map[string]json.RawMessage) (bool, error) {
	metadataRaw, ok := payload["metadata"]
	if !ok {
		return false, nil
	}

	var metadata v1.Metadata
	if err := json.Unmarshal(metadataRaw, &metadata); err != nil {
		return false, err
	}

	return metadata.DeletionTimestamp != "", nil
}

func validateEndpointCreateVGPU(store storage.Storage, input *endpointValidationInput) *validationError {
	if input.Patch.GetDeletionTimestamp() != "" {
		return nil
	}

	return validateEndpointVGPUEffective(store, input.New)
}

func validateEndpointPatchClusterImmutable(_ storage.Storage, input *endpointValidationInput) *validationError {
	return validateEndpointClusterImmutable(input)
}

func validateEndpointPatchVGPU(store storage.Storage, input *endpointValidationInput) *validationError {
	if !endpointPatchMayAffectVGPUValidation(&input.Patch) {
		return nil
	}

	return validateEndpointVGPUEffective(store, input.New)
}

func validateEndpointClusterImmutable(input *endpointValidationInput) *validationError {
	if endpointClusterChanged(input.Current, input.New) {
		return endpointClusterImmutableError()
	}

	return nil
}

func endpointClusterChanged(existing *v1.Endpoint, patch *v1.Endpoint) bool {
	if existing == nil || existing.Spec == nil {
		return false
	}

	if patch == nil || patch.Spec == nil {
		return existing.Spec.Cluster != ""
	}

	return existing.Spec.Cluster != patch.Spec.Cluster
}

func parseEndpointBody(body []byte) (*v1.Endpoint, *validationError) {
	var endpoint v1.Endpoint
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&endpoint); err != nil {
		return nil, invalidEndpointPayloadError(err)
	}

	return &endpoint, nil
}

// buildPostgrestEndpointPatchValidationNew mirrors the resource proxy before
// it forwards a PATCH: supplied top-level columns replace their current
// values. A supplied spec therefore replaces the complete PostgreSQL
// composite rather than recursively merging its attributes.
//
// Masked-column backfill only runs when the resource declares api:"-" masked
// fields (mirroring the proxy's len(excludeFields) > 0 guard). Endpoints have
// no masked fields; running the merge anyway would inject empty skeleton maps
// for every omitted sibling key and replace the current values with them.
func buildPostgrestEndpointPatchValidationNew(current *v1.Endpoint, body []byte) (*v1.Endpoint, error) {
	if current == nil {
		return nil, errors.New("current endpoint is required")
	}

	currentBody, err := json.Marshal(current)
	if err != nil {
		return nil, fmt.Errorf("marshal current endpoint: %w", err)
	}

	var currentPayload map[string]interface{}
	if err := json.Unmarshal(currentBody, &currentPayload); err != nil {
		return nil, fmt.Errorf("decode current endpoint: %w", err)
	}

	if len(bytes.TrimSpace(body)) == 0 {
		body = []byte(`{}`)
	}

	filteredBody, err := filterPayloadToTopLevelFields(
		body,
		extractTopLevelJSONFields(reflect.TypeOf(v1.Endpoint{})),
	)
	if err != nil {
		return nil, fmt.Errorf("filter endpoint patch payload: %w", err)
	}

	var patchPayload map[string]interface{}
	if err := json.Unmarshal(filteredBody, &patchPayload); err != nil {
		return nil, fmt.Errorf("decode endpoint patch payload: %w", err)
	}

	tagConfig := extractStructTagConfig(reflect.TypeOf(v1.Endpoint{}))
	if len(tagConfig.excludeFields) > 0 {
		mergeExcludedFields(patchPayload, currentPayload, tagConfig.excludeFields, tagConfig.arrayMergeKeys)
	}

	for field, value := range patchPayload {
		currentPayload[field] = value
	}

	nextBody, err := json.Marshal(currentPayload)
	if err != nil {
		return nil, fmt.Errorf("marshal patched endpoint: %w", err)
	}

	var next v1.Endpoint
	if err := json.Unmarshal(nextBody, &next); err != nil {
		return nil, fmt.Errorf("decode patched endpoint: %w", err)
	}

	return &next, nil
}

func validateEndpointVGPUEffective(store storage.Storage, endpoint *v1.Endpoint) *validationError {
	if endpoint == nil || endpoint.Spec == nil {
		return nil
	}

	if validationErr := validateEndpointReplicaCount(endpoint.Spec); validationErr != nil {
		return validationErr
	}

	if endpoint.Spec.Resources == nil || !endpoint.Spec.Resources.HasAcceleratorVirtualization() {
		return nil
	}

	if endpointReplicaCount(endpoint.Spec) == 0 {
		return nil
	}

	cluster, validationErr := resolveEndpointVGPUCluster(store, endpoint)
	if validationErr != nil {
		return validationErr
	}

	if validationErr := validateEndpointVGPUCluster(cluster); validationErr != nil {
		return validationErr
	}

	if validationErr := validateEndpointVGPUResourceShape(endpoint.Spec.Resources); validationErr != nil {
		return validationErr
	}

	if validationErr := validateEndpointVGPUMemorySpec(endpoint.Spec.Resources, cluster); validationErr != nil {
		return validationErr
	}

	// Capacity is intentionally left to scheduling/runtime status. Cluster
	// resource snapshots can be stale and should not be used as admission gates.
	return nil
}

func resolveEndpointPatch(
	store storage.Storage,
	queryParams url.Values,
) (*v1.Endpoint, *validationError) {
	if store == nil {
		return nil, internalServerValidationError()
	}

	filters := queryParamsToFilters(queryParams)
	if len(filters) == 0 {
		return nil, endpointPatchTargetError("endpoint lookup filters are required for endpoint PATCH")
	}

	endpoints, err := store.ListEndpoint(storage.ListOption{Filters: filters})
	if err != nil {
		return nil, internalServerValidationError()
	}

	if len(endpoints) == 0 {
		return nil, endpointPatchTargetError("endpoint not found for endpoint PATCH")
	}

	if len(endpoints) > 1 {
		return nil, endpointPatchTargetError("multiple endpoints matched endpoint PATCH filters")
	}

	return &endpoints[0], nil
}

func resolveEndpointVGPUCluster(store storage.Storage, endpoint *v1.Endpoint) (*v1.Cluster, *validationError) {
	if store == nil {
		return nil, internalServerValidationError()
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
		return nil, internalServerValidationError()
	}

	if len(clusters) == 0 {
		return nil, endpointVGPUTargetError(fmt.Sprintf("cluster %s/%s not found", workspace, clusterName))
	}

	if len(clusters) > 1 {
		return nil, endpointVGPUTargetError(fmt.Sprintf("multiple clusters matched %s/%s", workspace, clusterName))
	}

	return &clusters[0], nil
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

func endpointPatchTargetError(hint string) *validationError {
	return &validationError{
		Code:    "10221",
		Message: "invalid endpoint patch target",
		Hint:    hint,
	}
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

func endpointClusterImmutableError() *validationError {
	return &validationError{
		Code:    "10225",
		Message: "endpoint cluster is immutable",
		Hint:    "spec.cluster cannot be changed after endpoint creation",
	}
}
