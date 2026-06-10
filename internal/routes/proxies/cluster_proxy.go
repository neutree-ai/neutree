package proxies

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator"
	"github.com/neutree-ai/neutree/internal/middleware"
	"github.com/neutree-ai/neutree/pkg/storage"
)

func validateClusterDeletion(s storage.Storage) middleware.DeletionValidatorFunc {
	return func(workspace, name string) error {
		count, err := s.Count(storage.ENDPOINT_TABLE, []storage.Filter{
			{Column: "metadata->>workspace", Operator: "eq", Value: workspace},
			{Column: "spec->>cluster", Operator: "eq", Value: name},
		})
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

		if validationErr := validateClusterAcceleratorVirtualizationBody(c.Request.Method, body, c.Request.URL.RawQuery, s); validationErr != nil {
			c.JSON(http.StatusBadRequest, validationErr)
			c.Abort()

			return
		}

		c.Next()
	}
}

func validateClusterAcceleratorVirtualizationBody(
	method string,
	body []byte,
	rawQuery string,
	s storage.Storage,
) *validationError {
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

	if method == http.MethodPatch && (cluster.Spec.Type == "" || cluster.Spec.Version == "") {
		// Enabling virtualization through PATCH may only send the changed
		// accelerator_virtualization object. Load immutable cluster attributes
		// from the existing row before validating support.
		existingSpec, err := existingClusterSpec(rawQuery, s)
		if err != nil {
			return &validationError{
				Code:    "10208",
				Message: "spec.accelerator_virtualization is only supported for Kubernetes clusters",
				Hint:    err.Error(),
			}
		}

		if cluster.Spec.Type == "" {
			cluster.Spec.Type = existingSpec.Type
		}

		if cluster.Spec.Version == "" {
			cluster.Spec.Version = existingSpec.Version
		}
	}

	if cluster.Spec.Type != v1.KubernetesClusterType {
		return &validationError{
			Code:    "10208",
			Message: "spec.accelerator_virtualization is only supported for Kubernetes clusters",
			Hint:    "Use a Kubernetes cluster when enabling accelerator virtualization",
		}
	}

	if err := validateAcceleratorVirtualizationClusterVersion(cluster.Spec.Version); err != nil {
		return err
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

func validateAcceleratorVirtualizationClusterVersion(version string) *validationError {
	supported, err := accelerator.SupportsVirtualizationClusterVersion(version)
	if err != nil {
		return &validationError{
			Code:    "10209",
			Message: "invalid cluster version",
			Hint:    fmt.Sprintf("failed to parse spec.version %q: %v", version, err),
		}
	}

	if !supported {
		return &validationError{
			Code: "10208",
			Message: fmt.Sprintf("spec.accelerator_virtualization requires cluster version >= %s",
				accelerator.MinVirtualizationClusterVersion),
			Hint: fmt.Sprintf("Upgrade cluster version to %s or later before enabling accelerator virtualization",
				accelerator.MinVirtualizationClusterVersion),
		}
	}

	return nil
}

func existingClusterSpec(rawQuery string, s storage.Storage) (*v1.ClusterSpec, error) {
	if s == nil {
		return nil, fmt.Errorf("include spec.type and spec.version when enabling accelerator virtualization")
	}

	filters := filtersFromRawQuery(rawQuery)
	if len(filters) == 0 {
		return nil, fmt.Errorf("include spec.type and spec.version when enabling accelerator virtualization")
	}

	clusters, err := s.ListCluster(storage.ListOption{Filters: filters})
	if err != nil {
		return nil, fmt.Errorf("failed to load existing cluster: %w", err)
	}

	if len(clusters) != 1 {
		return nil, fmt.Errorf("cluster patch must match exactly one existing cluster")
	}

	if clusters[0].Spec == nil {
		return nil, fmt.Errorf("existing cluster has no spec")
	}

	return clusters[0].Spec, nil
}

func filtersFromRawQuery(rawQuery string) []storage.Filter {
	queryValues, err := url.ParseQuery(rawQuery)
	if err != nil {
		return nil
	}

	filters := make([]storage.Filter, 0, len(queryValues))
	columns := make([]string, 0, len(queryValues))

	for column := range queryValues {
		columns = append(columns, column)
	}

	sort.Strings(columns)

	for _, column := range columns {
		values := queryValues[column]
		if isNonFilterQueryParam(column) || len(values) == 0 {
			continue
		}

		operator, value, ok := strings.Cut(values[0], ".")
		if !ok || operator == "" || value == "" {
			continue
		}

		filters = append(filters, storage.Filter{
			Column:   column,
			Operator: operator,
			Value:    value,
		})
	}

	return filters
}

func isNonFilterQueryParam(key string) bool {
	switch key {
	case "select", "order", "limit", "offset", "columns":
		return true
	default:
		return false
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

	proxyGroup.GET("", handler)
	proxyGroup.POST("", acceleratorVirtualizationValidation, handler)
	proxyGroup.PATCH("", deletionValidation, acceleratorVirtualizationValidation, handler)
}
