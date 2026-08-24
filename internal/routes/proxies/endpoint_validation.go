package proxies

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"reflect"
	"slices"
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
			validateEndpointCreateModelSource,
			validateEndpointCreateResourceShape,
		},
	},
	endpointValidationPatch: {
		Validators: []endpointValidator{
			validateEndpointPatchClusterImmutable,
			validateEndpointPatchModelSource,
			validateEndpointPatchResourceShape,
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

func validateEndpointCreateModelSource(store storage.Storage, input *endpointValidationInput) *validationError {
	if input == nil || input.Patch.GetDeletionTimestamp() != "" {
		return nil
	}

	return validateEndpointModelSource(store, input.New)
}

func validateEndpointPatchModelSource(store storage.Storage, input *endpointValidationInput) *validationError {
	if input == nil || input.New == nil {
		return nil
	}

	if !endpointPatchMayAffectModelSourceValidation(&input.Patch) {
		return nil
	}

	return validateEndpointModelSource(store, input.New)
}

// endpointPatchMayAffectModelSourceValidation reports whether a PATCH can change
// the answer. spec.engine counts alongside spec.model: the engine decides whether
// a registry is required at all, so repointing an endpoint at another engine has
// to be re-checked even when spec.model is untouched.
func endpointPatchMayAffectModelSourceValidation(endpoint *v1.Endpoint) bool {
	if endpoint == nil || endpoint.Spec == nil {
		return false
	}

	return endpoint.Spec.Model != nil || endpoint.Spec.Engine != nil
}

// validateEndpointModelSource decides which parts of spec.model an endpoint has
// to fill in, and checks that the registry it names is real.
//
// Registry, model name and version are required exactly when Neutree downloads
// the model itself, which v1.IsBuiltInModelDownloaderEngine answers and the
// orchestrator keys its download step off as well -- keep the two on the same
// predicate, or validation and deployment will disagree about which endpoints
// need a model at all. An engine that brings its own model has none of these to
// give, and the database no longer demands them.
//
// A registry that *is* named is resolved against the endpoint's own workspace
// whichever engine is in play: deps.Storage runs as the service role and does
// not apply RLS, so a spec naming another workspace's registry would otherwise
// be honoured. The name *format*, when a name is given, stays a database
// concern -- it does not depend on the engine.
func validateEndpointModelSource(store storage.Storage, endpoint *v1.Endpoint) *validationError {
	if endpoint == nil || endpoint.Spec == nil || endpoint.Spec.Model == nil {
		return nil
	}

	model := endpoint.Spec.Model

	if endpointDownloadsModel(endpoint) {
		for _, required := range []struct {
			field string
			value string
		}{
			{"spec.model.registry", model.Registry},
			{"spec.model.name", model.Name},
			{"spec.model.version", model.Version},
		} {
			if required.value == "" {
				return endpointModelSourceError(fmt.Sprintf(
					"%s is required for engine %s, which downloads its own model",
					required.field, endpoint.Spec.Engine.Engine))
			}
		}
	}

	if model.Registry == "" {
		return nil
	}

	return validateEndpointModelRegistryVisible(store, endpoint)
}

// endpointDownloadsModel reports whether Neutree fetches this endpoint's model
// itself, which is what makes a model registry mandatory.
func endpointDownloadsModel(endpoint *v1.Endpoint) bool {
	return endpoint.Spec.Engine != nil &&
		v1.IsBuiltInModelDownloaderEngine(endpoint.Spec.Engine.Engine)
}

func validateEndpointModelRegistryVisible(store storage.Storage, endpoint *v1.Endpoint) *validationError {
	if store == nil {
		return internalServerValidationError()
	}

	workspace := endpointValidationWorkspace(endpoint)
	registry := endpoint.Spec.Model.Registry

	registries, err := store.ListModelRegistry(storage.ListOption{
		Filters: []storage.Filter{
			{Column: "metadata->name", Operator: "eq", Value: strconv.Quote(registry)},
			{Column: "metadata->workspace", Operator: "eq", Value: strconv.Quote(workspace)},
		},
	})
	if err != nil {
		return internalServerValidationError()
	}

	if len(registries) == 0 {
		return endpointModelSourceError(fmt.Sprintf("model registry %s/%s not found", workspace, registry))
	}

	return nil
}

func endpointValidationWorkspace(endpoint *v1.Endpoint) string {
	if endpoint != nil && endpoint.Metadata != nil && endpoint.Metadata.Workspace != "" {
		return endpoint.Metadata.Workspace
	}

	return defaultWorkspace
}

func validateEndpointCreateResourceShape(store storage.Storage, input *endpointValidationInput) *validationError {
	if input == nil || input.Patch.GetDeletionTimestamp() != "" {
		return nil
	}

	return validateEndpointResourceShape(store, input.New)
}

func validateEndpointPatchClusterImmutable(_ storage.Storage, input *endpointValidationInput) *validationError {
	return validateEndpointClusterImmutable(input)
}

func validateEndpointPatchResourceShape(store storage.Storage, input *endpointValidationInput) *validationError {
	if input == nil || input.New == nil {
		return nil
	}

	if !endpointPatchMayAffectResourceValidation(&input.Patch) {
		return nil
	}

	return validateEndpointResourceShape(store, input.New)
}

// endpointPatchMayAffectResourceValidation reports whether a patch could change
// the endpoint's resource shape (resources, target cluster, or replica count).
// Replica count is included because a paused endpoint skips all resource
// validation; cluster and resources are included because they gate the
// accelerator and product checks.
func endpointPatchMayAffectResourceValidation(endpoint *v1.Endpoint) bool {
	if endpoint == nil || endpoint.Spec == nil {
		return false
	}

	return endpoint.Spec.Resources != nil ||
		endpoint.Spec.Cluster != "" ||
		endpoint.Spec.Replicas.Num != nil
}

// validateEndpointResourceShape is the unified endpoint resource validator. It
// validates the replica count for every endpoint, then applies the shared
// accelerator declaration checks (a declared accelerator needs a type and a
// product), resolves the target cluster strictly, and applies the shared GPU
// card-count and product-support rules. Virtualization (vGPU) resources are
// then additionally checked against a ready, virtualization-enabled Kubernetes
// cluster, its supported virtualization resource keys, and the memory/core
// percent shape.
//
// The cluster must always resolve to exactly one entry; the virtualization
// path additionally enforces that the cluster is virtualization-ready.
func validateEndpointResourceShape(store storage.Storage, endpoint *v1.Endpoint) *validationError {
	if endpoint == nil || endpoint.Spec == nil {
		return nil
	}

	if validationErr := validateEndpointReplicaCount(endpoint.Spec); validationErr != nil {
		return validationErr
	}

	resources := endpoint.Spec.Resources
	if resources == nil || resources.Accelerator == nil {
		return nil
	}

	// A paused endpoint (zero replicas) skips all resource validation — the
	// pause action itself must not be blocked by the resource shape.
	if endpointReplicaCount(endpoint.Spec) == 0 {
		return nil
	}

	// Shared accelerator declaration checks, common to every resource shape.
	if validationErr := validateAcceleratorDeclaration(resources); validationErr != nil {
		return validationErr
	}

	cluster, validationErr := resolveEndpointCluster(store, endpoint)
	if validationErr != nil {
		return validationErr
	}

	// Shared GPU card-count rule, common to every resource shape. vGPU is
	// Kubernetes-only (enforced by validateEndpointVGPUCluster), so its cluster
	// type always yields the Kubernetes precision rule.
	clusterType := ""
	if cluster.Spec != nil {
		clusterType = cluster.Spec.Type
	}

	if validationErr := validateAcceleratorCount(resources.GPU, clusterType); validationErr != nil {
		return validationErr
	}

	// Shared product-support rule: the declared accelerator product must be
	// listed in the target cluster's accelerator metadata for the requested
	// type, for both virtualization and physical resources.
	if validationErr := validateAcceleratorProductSupported(cluster, resources); validationErr != nil {
		return validationErr
	}

	if resources.HasAcceleratorVirtualization() {
		// ---- virtualization (vGPU) resources ----
		if validationErr := validateEndpointVGPUCluster(cluster); validationErr != nil {
			return validationErr
		}

		if validationErr := validateEndpointVGPUResourcesSupported(resources, cluster); validationErr != nil {
			return validationErr
		}

		if validationErr := validateEndpointVGPUResourceShape(resources); validationErr != nil {
			return validationErr
		}

		if validationErr := validateEndpointVGPUMemorySpec(resources, cluster); validationErr != nil {
			return validationErr
		}

		// Capacity is intentionally left to scheduling/runtime status. Cluster
		// resource snapshots can be stale and should not be used as admission gates.
		return nil
	}

	return nil
}

// validateAcceleratorDeclaration enforces the accelerator declaration rules
// shared by every resource shape. When an accelerator block is present it must
// declare a type and a product. The card count is validated separately by
// validateAcceleratorCount, which needs the target cluster type.
func validateAcceleratorDeclaration(resources *v1.ResourceSpec) *validationError {
	if resources.Accelerator == nil {
		return nil
	}

	if resources.GetAcceleratorType() == "" {
		return endpointAcceleratorResourceError(errors.New("spec.resources.accelerator.type is required"))
	}

	if resources.GetAcceleratorProduct() == "" {
		return endpointAcceleratorResourceError(errors.New("spec.resources.accelerator.product is required"))
	}

	return nil
}

// validateAcceleratorCount enforces the GPU card-count rules shared by every
// resource shape. The count must be present, parseable and strictly greater
// than zero — a missing or malformed count would otherwise be read as "no
// accelerator" and silently drop the declaration — and must satisfy the
// cluster type's precision rule. Static (SSH) clusters allow one-decimal
// counts below one (0.1-0.9) and integers at or above one; Kubernetes clusters
// allow integers only. An unknown cluster type fails open on the precision
// rule (the rule cannot be determined) but still requires a present, parseable,
// strictly positive count.
func validateAcceleratorCount(gpu *string, clusterType string) *validationError {
	if gpu == nil || *gpu == "" {
		return endpointAcceleratorResourceError(errors.New("spec.resources.gpu must be a positive accelerator card count"))
	}

	count, err := strconv.ParseFloat(*gpu, 64)
	if err != nil || math.IsInf(count, 0) || math.IsNaN(count) || count <= 0 {
		return endpointAcceleratorResourceError(errors.New("spec.resources.gpu must be a positive accelerator card count"))
	}

	if !isAcceleratorCountPrecisionValid(count, clusterType) {
		if clusterType == string(v1.SSHClusterType) {
			return endpointAcceleratorResourceError(errors.New(
				"spec.resources.gpu must be a one-decimal value below 1 or an integer at or above 1",
			))
		}

		return endpointAcceleratorResourceError(errors.New("spec.resources.gpu must be a positive integer"))
	}

	return nil
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

// resolveEndpointCluster resolves the endpoint's target cluster for resource
// validation. The cluster must resolve to exactly one entry: an unresolvable
// cluster is a malformed request, not a request to skip validation. An
// infrastructure failure while looking up the cluster is surfaced as an
// internal error rather than accepted silently.
func resolveEndpointCluster(store storage.Storage, endpoint *v1.Endpoint) (*v1.Cluster, *validationError) {
	if store == nil {
		return nil, internalServerValidationError()
	}

	if endpoint == nil || endpoint.Spec == nil {
		return nil, endpointAcceleratorResourceError(errors.New("spec.cluster is required"))
	}

	clusterName := endpoint.Spec.Cluster
	if clusterName == "" {
		return nil, endpointAcceleratorResourceError(errors.New("spec.cluster is required"))
	}

	workspace := endpointValidationWorkspace(endpoint)

	clusters, err := store.ListCluster(storage.ListOption{
		Filters: endpointClusterLookupFilters(clusterName, workspace),
	})
	if err != nil {
		return nil, internalServerValidationError()
	}

	if len(clusters) == 0 {
		return nil, endpointAcceleratorResourceError(fmt.Errorf("cluster %s/%s not found", workspace, clusterName))
	}

	if len(clusters) > 1 {
		return nil, endpointAcceleratorResourceError(fmt.Errorf("multiple clusters matched %s/%s", workspace, clusterName))
	}

	return &clusters[0], nil
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

// validateEndpointVGPUResourcesSupported rejects virtualization resource keys on
// endpoints when the cluster's effective accelerator virtualization mode does
// not support them. The cluster status block lists the supported resources
// (supported_resources) for the active mode; any virtualization.* key the
// endpoint requests that is not in that list is rejected. The value of each
// key is not inspected here — value range is enforced by the shape validator.
// A missing capability block (stale cluster) falls back to shape-only
// validation.
func validateEndpointVGPUResourcesSupported(resources *v1.ResourceSpec, cluster *v1.Cluster) *validationError {
	if resources == nil || !resources.HasAcceleratorVirtualization() {
		return nil
	}

	if cluster == nil || cluster.Status == nil || cluster.Status.AcceleratorVirtualization == nil {
		return nil
	}

	supported := cluster.Status.AcceleratorVirtualization.SupportedResources

	for key := range resources.Accelerator {
		if !v1.IsAcceleratorVirtualizationKey(key) {
			continue
		}

		if !slices.Contains(supported, key) {
			return &validationError{
				Code:    "10227",
				Message: fmt.Sprintf("virtualization key %q is not supported by the cluster accelerator virtualization mode", key),
				Hint:    fmt.Sprintf("virtualization.%s is not allowed under the active cluster virtualization mode; switch the cluster mode, or remove the setting", key),
			}
		}
	}

	return nil
}

func validateEndpointVGPUResourceShape(resources *v1.ResourceSpec) *validationError {
	if !resources.HasAcceleratorVirtualization() {
		return nil
	}

	// Accelerator type/product presence is enforced by the shared
	// validateAcceleratorDeclaration and the card count by the shared
	// validateAcceleratorCount; this function only checks the
	// virtualization-specific shape (memory, core percent).

	if resources.GetAcceleratorVirtualizationMemoryPercent() != "" {
		return &validationError{
			Code:    "10219",
			Message: "virtualization memory_percent is not supported",
			Hint:    "Set virtualization.memory_mib instead of virtualization.memory_percent",
		}
	}

	if _, err := parseRequiredPositiveInteger(resources.GetAcceleratorVirtualizationMemoryMiB(), "virtualization.memory_mib"); err != nil {
		return endpointResourceValueError(err)
	}

	if err := validateOneToHundredPercentResource(resources.GetAcceleratorVirtualizationCorePercent(), "virtualization.core_percent"); err != nil {
		return endpointResourceValueError(err)
	}

	return nil
}

// isAcceleratorCountPrecisionValid reports whether an accelerator card count
// satisfies the strict-positivity and precision rules for a cluster type. The
// count must be strictly greater than zero — a zero count means "no
// accelerator", which is expressed by omitting the accelerator type, not by a
// zero count — and must satisfy the type-specific precision rule. Static (SSH)
// clusters allow one-decimal counts below one (0.1-0.9) and integers at or
// above one; Kubernetes clusters allow integers only. An unknown cluster type
// fails open (accepts) because the rule cannot be determined.
func isAcceleratorCountPrecisionValid(count float64, clusterType string) bool {
	// +Inf, -Inf and NaN never represent a real card count; reject them before
	// the per-type rules (which +Inf would otherwise pass as an "integer").
	if math.IsInf(count, 0) || math.IsNaN(count) {
		return false
	}

	// The count must be strictly positive.
	if count <= 0 {
		return false
	}

	switch clusterType {
	case string(v1.KubernetesClusterType):
		return count == math.Trunc(count)
	case string(v1.SSHClusterType):
		if count >= 1 {
			return count == math.Trunc(count)
		}

		// 0 < count < 1: exactly one decimal place.
		scaled := count * 10

		return math.Abs(scaled-math.Round(scaled)) < 1e-9
	default:
		return true
	}
}

// validateAcceleratorProductSupported rejects an accelerator product that the
// target cluster's accelerator metadata does not list for the requested
// accelerator type. When the cluster's metadata is unavailable the check fails
// open, so requests against clusters that have not reported metadata are not
// rejected.
func validateAcceleratorProductSupported(cluster *v1.Cluster, resources *v1.ResourceSpec) *validationError {
	resourceInfo := clusterResourceInfo(cluster)
	if resourceInfo == nil || resourceInfo.AcceleratorMetadata == nil {
		return nil
	}

	metadata := resourceInfo.AcceleratorMetadata[v1.AcceleratorType(resources.GetAcceleratorType())]
	// A nil product map means the cluster reported no product metadata for the
	// type, so support cannot be judged and the check fails open. A present but
	// empty map is authoritative: the cluster reports the type with no products,
	// so every product is rejected (same empty-is-authoritative contract as
	// validateEndpointVGPUResourcesSupported's supported_resources list).
	if metadata == nil || metadata.Products == nil {
		return nil
	}

	if _, ok := metadata.Products[v1.AcceleratorProduct(resources.GetAcceleratorProduct())]; !ok {
		return endpointAcceleratorResourceError(fmt.Errorf(
			"unsupported accelerator product %q for accelerator type %q",
			resources.GetAcceleratorProduct(),
			resources.GetAcceleratorType(),
		))
	}

	return nil
}

func endpointAcceleratorResourceError(err error) *validationError {
	return &validationError{
		Code:    "10230",
		Message: "invalid endpoint accelerator resources",
		Hint:    err.Error(),
	}
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

	acceleratorType := resources.GetAcceleratorType()
	product := resources.GetAcceleratorProduct()
	maxMemoryMiB, ok := clusterProductMaxMemoryMiB(cluster, acceleratorType, product)

	if !ok {
		return nil
	}

	if requestedMemoryMiB > maxMemoryMiB {
		return endpointResourceValueError(fmt.Errorf(
			"virtualization.memory_mib must be less than or equal to physical accelerator memory_mib %d for accelerator product %s",
			maxMemoryMiB,
			product,
		))
	}

	return nil
}

func clusterProductMaxMemoryMiB(cluster *v1.Cluster, acceleratorType string, product string) (int64, bool) {
	resourceInfo := clusterResourceInfo(cluster)
	if resourceInfo == nil {
		return 0, false
	}

	return clusterProductMetadataMemoryMiB(resourceInfo, acceleratorType, product)
}

func clusterProductMetadataMemoryMiB(
	resourceInfo *v1.ClusterResources,
	acceleratorType string,
	product string,
) (int64, bool) {
	if resourceInfo.AcceleratorMetadata == nil {
		return 0, false
	}

	metadata := resourceInfo.AcceleratorMetadata[v1.AcceleratorType(acceleratorType)]
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

func parseRequiredPositiveInteger(value string, field string) (int64, error) {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", field)
	}

	return parsed, nil
}

func validateOneToHundredPercentResource(value string, field string) error {
	if value == "" {
		return nil
	}

	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 1 || parsed > 100 {
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

func endpointModelSourceError(hint string) *validationError {
	return &validationError{
		Code:    "10229",
		Message: "invalid endpoint model source",
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
