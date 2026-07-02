package proxies

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	clustervalidation "github.com/neutree-ai/neutree/internal/cluster/validation"
	"github.com/neutree-ai/neutree/internal/middleware"
	"github.com/neutree-ai/neutree/pkg/storage"
)

func validateClusterDeletion(s storage.Storage) middleware.DeletionValidatorFunc {
	return func(workspace, name string) error {
		count, err := s.Count(storage.ENDPOINT_TABLE, clusterEndpointReferenceFilters(workspace, name))
		if err != nil {
			return fmt.Errorf("failed to count endpoints: %w", err)
		}

		if count > 0 {
			return &middleware.DeletionError{
				Code:    "10126",
				Message: fmt.Sprintf("cannot delete cluster '%s/%s'", workspace, name),
				Hint:    fmt.Sprintf("%d endpoint(s) still reference this cluster", count),
			}
		}

		return nil
	}
}

func validateClusterAcceleratorVirtualization(s storage.Storage) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method != http.MethodPost && c.Request.Method != http.MethodPatch {
			c.Next()
			return
		}

		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.JSON(http.StatusBadRequest, &validationError{
				Code:    "10209",
				Message: "invalid cluster payload",
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

		if validationErr := validateClusterAcceleratorVirtualizationBody(body); validationErr != nil {
			c.JSON(http.StatusBadRequest, validationErr)
			c.Abort()

			return
		}

		if c.Request.Method == http.MethodPatch {
			disableRequested, err := clusterAcceleratorVirtualizationDisableRequested(body)
			if err != nil {
				c.JSON(http.StatusBadRequest, invalidClusterPayloadError(err))
				c.Abort()

				return
			}

			if !disableRequested {
				c.Next()
				return
			}

			var cluster v1.Cluster
			if err := json.Unmarshal(body, &cluster); err != nil {
				c.JSON(http.StatusBadRequest, invalidClusterPayloadError(err))
				c.Abort()

				return
			}

			if validationErr := validateClusterAcceleratorVirtualizationDisable(
				s, cluster, c.Request.URL.Query()); validationErr != nil {
				c.JSON(http.StatusBadRequest, validationErr)
				c.Abort()

				return
			}
		}

		c.Next()
	}
}

func validateClusterVersionUpdate(s storage.Storage) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method != http.MethodPatch {
			c.Next()
			return
		}

		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.JSON(http.StatusBadRequest, invalidClusterPayloadError(err))
			c.Abort()

			return
		}

		c.Request.Body = io.NopCloser(bytes.NewReader(body))

		if len(bytes.TrimSpace(body)) == 0 {
			c.Next()
			return
		}

		desiredVersion, hasVersion, err := clusterPatchDesiredVersion(body)
		if err != nil {
			c.JSON(http.StatusBadRequest, invalidClusterPayloadError(err))
			c.Abort()

			return
		}

		if !hasVersion {
			c.Next()
			return
		}

		var patch v1.Cluster
		if err := json.Unmarshal(body, &patch); err != nil {
			c.JSON(http.StatusBadRequest, invalidClusterPayloadError(err))
			c.Abort()

			return
		}

		if patch.GetDeletionTimestamp() != "" {
			c.Next()
			return
		}

		current, validationErr := resolveClusterForVersionUpdate(s, patch, c.Request.URL.Query())
		if validationErr != nil {
			c.JSON(http.StatusBadRequest, validationErr)
			c.Abort()

			return
		}

		clusterType := current.Spec.Type
		if patch.Spec != nil && patch.Spec.Type != "" {
			clusterType = patch.Spec.Type
		}

		if validationErr := validateStaticNodeClusterFlowDowngrade(clusterType, current.GetVersion(), desiredVersion); validationErr != nil {
			c.JSON(http.StatusBadRequest, validationErr)
			c.Abort()

			return
		}

		c.Next()
	}
}

func clusterPatchDesiredVersion(body []byte) (string, bool, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", false, err
	}

	specRaw, ok := payload["spec"]
	if !ok {
		return "", false, nil
	}

	var spec map[string]json.RawMessage
	if err := json.Unmarshal(specRaw, &spec); err != nil {
		return "", false, err
	}

	versionRaw, ok := spec["version"]
	if !ok {
		return "", false, nil
	}

	var version string
	if err := json.Unmarshal(versionRaw, &version); err != nil {
		return "", false, err
	}

	return version, true, nil
}

func resolveClusterForVersionUpdate(
	s storage.Storage, patch v1.Cluster, queryParams url.Values,
) (*v1.Cluster, *validationError) {
	filters := queryParamsToFilters(queryParams)
	if len(filters) == 0 && patch.Metadata != nil && patch.Metadata.Workspace != "" && patch.Metadata.Name != "" {
		filters = []storage.Filter{
			{Column: "metadata->>workspace", Operator: "eq", Value: patch.Metadata.Workspace},
			{Column: "metadata->>name", Operator: "eq", Value: patch.Metadata.Name},
		}
	}

	if len(filters) == 0 {
		return nil, &validationError{
			Code:    "10212",
			Message: "failed to validate cluster version update",
			Hint:    "cluster identity is required when updating spec.version",
		}
	}

	clusters, err := s.ListCluster(storage.ListOption{Filters: filters})
	if err != nil {
		return nil, &validationError{
			Code:    "10212",
			Message: "failed to validate cluster version update",
			Hint:    err.Error(),
		}
	}

	if len(clusters) != 1 {
		return nil, &validationError{
			Code:    "10212",
			Message: "failed to validate cluster version update",
			Hint:    fmt.Sprintf("expected exactly one cluster from patch filters, got %d", len(clusters)),
		}
	}

	current := &clusters[0]
	if current.Spec == nil {
		return nil, &validationError{
			Code:    "10212",
			Message: "failed to validate cluster version update",
			Hint:    "current cluster spec is required",
		}
	}

	return current, nil
}

func validateStaticNodeClusterFlowDowngrade(clusterType, previousVersion, desiredVersion string) *validationError {
	if err := clustervalidation.ValidateStaticNodeClusterFlowVersionUpdate(
		clusterType, previousVersion, desiredVersion); err != nil {
		return &validationError{
			Code:    "10212",
			Message: "invalid cluster version update",
			Hint:    err.Error(),
		}
	}

	return nil
}

func clusterAcceleratorVirtualizationDisableRequested(body []byte) (bool, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return false, err
	}

	specRaw, ok := payload["spec"]
	if !ok {
		return false, nil
	}

	var spec map[string]json.RawMessage
	if err := json.Unmarshal(specRaw, &spec); err != nil {
		return false, err
	}

	acceleratorVirtualizationRaw, ok := spec["accelerator_virtualization"]
	if !ok {
		return false, nil
	}

	var acceleratorVirtualization map[string]json.RawMessage
	if err := json.Unmarshal(acceleratorVirtualizationRaw, &acceleratorVirtualization); err != nil {
		return false, err
	}

	enabledRaw, ok := acceleratorVirtualization["enabled"]
	if !ok {
		// The CLI decodes YAML into Cluster and re-marshals JSON before PATCH.
		// enabled:false is omitted by the API struct tag, yielding
		// "accelerator_virtualization": {}, which still clears the enabled flag.
		return true, nil
	}

	var enabled bool
	if err := json.Unmarshal(enabledRaw, &enabled); err != nil {
		return false, err
	}

	return !enabled, nil
}

func clusterEndpointReferenceFilters(workspace, name string) []storage.Filter {
	return []storage.Filter{
		{Column: "metadata->>workspace", Operator: "eq", Value: workspace},
		{Column: "spec->>cluster", Operator: "eq", Value: name},
	}
}

func validateClusterAcceleratorVirtualizationDisable(
	s storage.Storage, cluster v1.Cluster, queryParams url.Values,
) *validationError {
	if cluster.GetDeletionTimestamp() != "" {
		return nil
	}

	workspace, name, validationErr := resolveClusterIdentityForAcceleratorVirtualizationDisable(
		s, cluster, queryParams)
	if validationErr != nil {
		return validationErr
	}

	endpoints, err := s.ListEndpoint(storage.ListOption{Filters: clusterEndpointReferenceFilters(workspace, name)})
	if err != nil {
		return &validationError{
			Code:    "10209",
			Message: "failed to validate cluster accelerator virtualization",
			Hint:    err.Error(),
		}
	}

	vGPUEndpointCount := 0

	for _, endpoint := range endpoints {
		if endpoint.Spec != nil &&
			endpoint.Spec.Resources != nil &&
			endpoint.Spec.Resources.HasAcceleratorVirtualization() {
			vGPUEndpointCount++
		}
	}

	if vGPUEndpointCount > 0 {
		return &validationError{
			Code:    "10211",
			Message: fmt.Sprintf("cannot disable accelerator virtualization for cluster '%s/%s'", workspace, name),
			Hint: fmt.Sprintf(
				"%d vGPU endpoint(s) still reference this cluster; delete the vGPU endpoints before disabling accelerator virtualization",
				vGPUEndpointCount,
			),
		}
	}

	return nil
}

func resolveClusterIdentityForAcceleratorVirtualizationDisable(
	s storage.Storage, cluster v1.Cluster, queryParams url.Values,
) (string, string, *validationError) {
	filters := queryParamsToFilters(queryParams)
	if len(filters) > 0 {
		workspace, name, validationErr := resolveClusterIdentityFromPatchFilters(s, filters)
		if validationErr != nil {
			return "", "", validationErr
		}

		if cluster.Metadata != nil &&
			((cluster.Metadata.Workspace != "" && cluster.Metadata.Workspace != workspace) ||
				(cluster.Metadata.Name != "" && cluster.Metadata.Name != name)) {
			return "", "", &validationError{
				Code:    "10209",
				Message: "failed to validate cluster accelerator virtualization",
				Hint:    "cluster metadata in patch body does not match patch target",
			}
		}

		return workspace, name, nil
	}

	if cluster.Metadata != nil && cluster.Metadata.Workspace != "" && cluster.Metadata.Name != "" {
		return cluster.Metadata.Workspace, cluster.Metadata.Name, nil
	}

	return "", "", &validationError{
		Code:    "10209",
		Message: "failed to validate cluster accelerator virtualization",
		Hint:    "cluster identity is required when disabling accelerator virtualization",
	}
}

func resolveClusterIdentityFromPatchFilters(s storage.Storage, filters []storage.Filter) (string, string, *validationError) {
	clusters, err := s.ListCluster(storage.ListOption{Filters: filters})
	if err != nil {
		return "", "", &validationError{
			Code:    "10209",
			Message: "failed to validate cluster accelerator virtualization",
			Hint:    err.Error(),
		}
	}

	if len(clusters) != 1 {
		return "", "", &validationError{
			Code:    "10209",
			Message: "failed to validate cluster accelerator virtualization",
			Hint:    fmt.Sprintf("expected exactly one cluster from patch filters, got %d", len(clusters)),
		}
	}

	resolved := clusters[0]
	if resolved.Metadata == nil || resolved.Metadata.Workspace == "" || resolved.Metadata.Name == "" {
		return "", "", &validationError{
			Code:    "10209",
			Message: "failed to validate cluster accelerator virtualization",
			Hint:    "cluster identity is required when disabling accelerator virtualization",
		}
	}

	return resolved.Metadata.Workspace, resolved.Metadata.Name, nil
}

func validateClusterAcceleratorVirtualizationBody(body []byte) *validationError {
	var cluster v1.Cluster
	decoder := json.NewDecoder(bytes.NewReader(body))

	if err := decoder.Decode(&cluster); err != nil {
		return invalidClusterPayloadError(err)
	}

	if cluster.GetDeletionTimestamp() != "" {
		// Soft delete PATCH reuses the same route but should not be blocked by
		// accelerator virtualization validation.
		return nil
	}

	if cluster.Spec == nil || cluster.Spec.AcceleratorVirtualization == nil {
		return nil
	}

	if !cluster.Spec.AcceleratorVirtualization.Enabled {
		return nil
	}

	if err := clustervalidation.ValidateAcceleratorVirtualizationConfigPatch(
		cluster.Spec.AcceleratorVirtualization.ConfigPatch); err != nil {
		return acceleratorVirtualizationValidationError(err)
	}

	if err := clustervalidation.ValidateAcceleratorVirtualizationClusterSupport(
		cluster.Spec.Type, cluster.Spec.Version); err != nil {
		return acceleratorVirtualizationValidationError(err)
	}

	return nil
}

func invalidClusterPayloadError(err error) *validationError {
	return &validationError{
		Code:    "10209",
		Message: "invalid cluster payload",
		Hint:    err.Error(),
	}
}

func acceleratorVirtualizationValidationError(err error) *validationError {
	var virtualizationErr *clustervalidation.AcceleratorVirtualizationError
	if !errors.As(err, &virtualizationErr) {
		return &validationError{
			Code:    "10209",
			Message: "invalid accelerator virtualization config",
			Hint:    err.Error(),
		}
	}

	switch virtualizationErr.Reason {
	case clustervalidation.AcceleratorVirtualizationInvalidVersionReason:
		return &validationError{
			Code:    "10209",
			Message: virtualizationErr.Message,
			Hint:    virtualizationErr.Hint,
		}
	case clustervalidation.AcceleratorVirtualizationUnsupportedClusterReason,
		clustervalidation.AcceleratorVirtualizationUnsupportedVersionReason:
		return &validationError{
			Code:    "10208",
			Message: virtualizationErr.Message,
			Hint:    virtualizationErr.Hint,
		}
	default:
		return &validationError{
			Code:    "10210",
			Message: virtualizationErr.Message,
			Hint:    virtualizationErr.Hint,
		}
	}
}

func RegisterClusterRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) {
	proxyGroup := group.Group("/clusters")
	proxyGroup.Use(middlewares...)

	deletionValidation := middleware.DeletionValidation(
		storage.CLUSTERS_TABLE,
		validateClusterDeletion(deps.Storage),
	)
	handler := CreateStructProxyHandler[v1.Cluster](deps, storage.CLUSTERS_TABLE)
	acceleratorVirtualizationValidation := validateClusterAcceleratorVirtualization(deps.Storage)
	versionUpdateValidation := validateClusterVersionUpdate(deps.Storage)

	proxyGroup.GET("", handler)
	proxyGroup.POST("", acceleratorVirtualizationValidation, handler)
	proxyGroup.PATCH("", deletionValidation, versionUpdateValidation, acceleratorVirtualizationValidation, handler)
}
