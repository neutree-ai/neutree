package proxies

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	clustervalidation "github.com/neutree-ai/neutree/internal/cluster/validation"
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

func validateClusterAcceleratorVirtualization() gin.HandlerFunc {
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

		c.Next()
	}
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
	acceleratorVirtualizationValidation := validateClusterAcceleratorVirtualization()

	proxyGroup.GET("", handler)
	proxyGroup.POST("", acceleratorVirtualizationValidation, StampCredentialOwnerLabel(), handler)
	proxyGroup.PATCH("", deletionValidation, acceleratorVirtualizationValidation, handler)
}
