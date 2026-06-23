package proxies

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

func validateEndpointAcceleratorVirtualization(store storage.Storage) gin.HandlerFunc {
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

		if validationErr := validateEndpointAcceleratorVirtualizationRequest(
			store,
			c.Request.Method,
			c.Request.URL.Query(),
			body,
		); validationErr != nil {
			c.JSON(validationErrStatus(validationErr), validationErr)
			c.Abort()

			return
		}

		c.Next()
	}
}

func validateEndpointAcceleratorVirtualizationRequest(
	store storage.Storage,
	method string,
	queryParams url.Values,
	body []byte,
) *validationError {
	endpoint, validationErr := decodeEndpointAcceleratorVirtualizationBody(body)
	if validationErr != nil {
		return validationErr
	}

	if validationErr := validateEndpointAcceleratorVirtualizationEndpoint(endpoint); validationErr != nil {
		return validationErr
	}

	return validateEndpointAcceleratorVirtualizationPreflight(store, method, queryParams, endpoint)
}

func decodeEndpointAcceleratorVirtualizationBody(body []byte) (*v1.Endpoint, *validationError) {
	var endpoint v1.Endpoint
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&endpoint); err != nil {
		return nil, invalidEndpointPayloadError(err)
	}

	return &endpoint, nil
}

func validateEndpointAcceleratorVirtualizationBody(body []byte) *validationError {
	endpoint, validationErr := decodeEndpointAcceleratorVirtualizationBody(body)
	if validationErr != nil {
		return validationErr
	}

	return validateEndpointAcceleratorVirtualizationEndpoint(endpoint)
}

func validateEndpointAcceleratorVirtualizationEndpoint(endpoint *v1.Endpoint) *validationError {
	if endpoint == nil {
		return nil
	}

	if endpoint.GetDeletionTimestamp() != "" {
		// Soft delete PATCH reuses the same route but should not be blocked by
		// endpoint resource validation.
		return nil
	}

	if endpoint.Spec == nil || endpoint.Spec.Resources == nil {
		return nil
	}

	resources := endpoint.Spec.Resources
	if err := validateEndpointAcceleratorVirtualizationResourceShape(resources); err != nil {
		return err
	}

	return nil
}

func validateEndpointAcceleratorVirtualizationPreflight(
	store storage.Storage,
	method string,
	queryParams url.Values,
	endpoint *v1.Endpoint,
) *validationError {
	if endpoint == nil {
		return nil
	}

	if endpoint.GetDeletionTimestamp() != "" {
		return nil
	}

	if method == http.MethodPatch &&
		(endpoint.Spec == nil || endpoint.Spec.Resources == nil || !endpoint.Spec.Resources.HasAcceleratorVirtualization()) {
		return nil
	}

	if method != http.MethodPatch &&
		(endpoint.Spec == nil || endpoint.Spec.Resources == nil || !endpoint.Spec.Resources.HasAcceleratorVirtualization()) {
		return nil
	}

	if store == nil {
		return endpointAcceleratorVirtualizationLookupError("storage is required to validate endpoint accelerator virtualization")
	}

	if method == http.MethodPatch && endpointPatchNeedsExistingEndpoint(endpoint, queryParams) {
		existing, validationErr := resolveEndpointAcceleratorVirtualizationPatchEndpoint(store, queryParams)
		if validationErr != nil {
			return validationErr
		}

		merged := mergeEndpointAcceleratorVirtualizationPatch(existing, endpoint)
		endpoint = &merged
	}

	if endpoint.Spec == nil || endpoint.Spec.Resources == nil || !endpoint.Spec.Resources.HasAcceleratorVirtualization() {
		return nil
	}

	target, validationErr := resolveEndpointAcceleratorVirtualizationTarget(method, queryParams, endpoint)
	if validationErr != nil {
		return validationErr
	}

	clusters, err := store.ListCluster(storage.ListOption{
		Filters: endpointClusterLookupFilters(target.cluster, target.workspace),
	})
	if err != nil {
		klog.Warningf("failed to look up endpoint accelerator virtualization cluster %s/%s: %v", target.workspace, target.cluster, err)

		return endpointAcceleratorVirtualizationLookupError("failed to look up cluster for endpoint accelerator virtualization")
	}

	if len(clusters) == 0 {
		return endpointAcceleratorVirtualizationTargetError(fmt.Sprintf("cluster %s/%s not found", target.workspace, target.cluster))
	}

	if len(clusters) > 1 {
		return endpointAcceleratorVirtualizationTargetError(fmt.Sprintf("multiple clusters matched %s/%s", target.workspace, target.cluster))
	}

	if validationErr := validateEndpointClusterAcceleratorVirtualizationReady(&clusters[0]); validationErr != nil {
		return validationErr
	}

	return nil
}

type endpointAcceleratorVirtualizationTarget struct {
	cluster   string
	workspace string
}

func resolveEndpointAcceleratorVirtualizationTarget(
	method string,
	queryParams url.Values,
	endpoint *v1.Endpoint,
) (endpointAcceleratorVirtualizationTarget, *validationError) {
	target := endpointAcceleratorVirtualizationTarget{
		workspace: endpointTargetWorkspace(method, queryParams, endpoint),
	}

	if endpoint.Spec != nil {
		target.cluster = endpoint.Spec.Cluster
	}

	return validateEndpointAcceleratorVirtualizationTarget(target)
}

func resolveEndpointAcceleratorVirtualizationPatchEndpoint(
	store storage.Storage,
	queryParams url.Values,
) (*v1.Endpoint, *validationError) {
	filters := queryParamsToFilters(queryParams)
	if len(filters) == 0 {
		return nil, endpointAcceleratorVirtualizationTargetError("endpoint lookup filters are required for vGPU resource PATCH")
	}

	endpoints, err := store.ListEndpoint(storage.ListOption{Filters: filters})
	if err != nil {
		klog.Warningf("failed to look up endpoint for vGPU resource PATCH: %v", err)

		return nil, endpointAcceleratorVirtualizationLookupError("failed to look up endpoint for vGPU resource PATCH")
	}

	if len(endpoints) == 0 {
		return nil, endpointAcceleratorVirtualizationTargetError("endpoint not found for vGPU resource PATCH")
	}

	if len(endpoints) > 1 {
		return nil, endpointAcceleratorVirtualizationTargetError("multiple endpoints matched vGPU resource PATCH filters")
	}

	return &endpoints[0], nil
}

func endpointPatchNeedsExistingEndpoint(endpoint *v1.Endpoint, queryParams url.Values) bool {
	return endpoint != nil &&
		endpoint.Spec != nil &&
		endpoint.Spec.Resources != nil &&
		endpoint.Spec.Resources.HasAcceleratorVirtualization() &&
		(endpoint.Spec.Cluster == "" || endpointTargetWorkspace(http.MethodPatch, queryParams, endpoint) == "")
}

func mergeEndpointAcceleratorVirtualizationPatch(existing *v1.Endpoint, patch *v1.Endpoint) v1.Endpoint {
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
	}

	return merged
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

func validateEndpointAcceleratorVirtualizationTarget(
	target endpointAcceleratorVirtualizationTarget,
) (endpointAcceleratorVirtualizationTarget, *validationError) {
	if target.workspace == "" {
		target.workspace = defaultWorkspace
	}

	if target.cluster == "" {
		return target, endpointAcceleratorVirtualizationTargetError("spec.cluster is required for endpoint accelerator virtualization")
	}

	return target, nil
}

func endpointTargetWorkspace(method string, queryParams url.Values, endpoint *v1.Endpoint) string {
	if workspace := endpointWorkspace(endpoint); workspace != "" {
		return workspace
	}

	if method == http.MethodPatch {
		return endpointWorkspaceFromQuery(queryParams)
	}

	return ""
}

func endpointWorkspace(endpoint *v1.Endpoint) string {
	if endpoint == nil || endpoint.Metadata == nil {
		return ""
	}

	return endpoint.Metadata.Workspace
}

func endpointWorkspaceFromQuery(queryParams url.Values) string {
	for _, key := range []string{"metadata->>workspace", "metadata->workspace"} {
		values, ok := queryParams[key]
		if !ok || len(values) == 0 {
			continue
		}

		workspace := values[0]

		parts := strings.SplitN(workspace, ".", 2)
		if len(parts) == 2 {
			if parts[0] != "eq" {
				continue
			}

			workspace = parts[1]
		}

		if unquoted, err := strconv.Unquote(workspace); err == nil {
			workspace = unquoted
		}

		return workspace
	}

	return ""
}

func endpointClusterLookupFilters(cluster, workspace string) []storage.Filter {
	return []storage.Filter{
		{Column: "metadata->name", Operator: "eq", Value: strconv.Quote(cluster)},
		{Column: "metadata->workspace", Operator: "eq", Value: strconv.Quote(workspace)},
	}
}

func validateEndpointClusterAcceleratorVirtualizationReady(cluster *v1.Cluster) *validationError {
	if cluster == nil || cluster.Spec == nil || !cluster.Spec.AcceleratorVirtualizationEnabled() {
		return endpointAcceleratorVirtualizationNotReadyError(cluster, "cluster accelerator virtualization is not enabled")
	}

	if cluster.Spec.Type != v1.KubernetesClusterType {
		return endpointAcceleratorVirtualizationNotReadyError(cluster, "endpoint accelerator virtualization is only supported for kubernetes clusters")
	}

	if cluster.Status == nil || cluster.Status.ComponentStatus == nil {
		return endpointAcceleratorVirtualizationNotReadyError(cluster, "cluster accelerator virtualization component status is missing")
	}

	component := cluster.Status.ComponentStatus[v1.ComponentStatusAcceleratorVirtualizationKey]
	if component == nil {
		return endpointAcceleratorVirtualizationNotReadyError(cluster, "cluster accelerator virtualization component status is missing")
	}

	if component.Phase != v1.ComponentPhaseReady {
		hint := "cluster accelerator virtualization is not ready"
		if component.Reason != "" || component.Message != "" {
			hint = fmt.Sprintf("%s: %s %s", hint, component.Reason, component.Message)
		}

		return endpointAcceleratorVirtualizationNotReadyError(cluster, hint)
	}

	return nil
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

func endpointAcceleratorVirtualizationTargetError(hint string) *validationError {
	return &validationError{
		Code:    "10221",
		Message: "invalid endpoint accelerator virtualization target",
		Hint:    hint,
	}
}

func endpointAcceleratorVirtualizationLookupError(hint string) *validationError {
	err := endpointAcceleratorVirtualizationTargetError(hint)
	err.HTTPStatus = http.StatusServiceUnavailable

	return err
}

func validationErrStatus(err *validationError) int {
	if err != nil && err.HTTPStatus != 0 {
		return err.HTTPStatus
	}

	return http.StatusBadRequest
}

func endpointAcceleratorVirtualizationNotReadyError(cluster *v1.Cluster, hint string) *validationError {
	if cluster != nil && cluster.Metadata != nil {
		hint = fmt.Sprintf("cluster %s/%s accelerator virtualization is not ready: %s", cluster.Metadata.Workspace, cluster.Metadata.Name, hint)
	}

	return &validationError{
		Code:    "10222",
		Message: "cluster accelerator virtualization is not ready",
		Hint:    hint,
	}
}
