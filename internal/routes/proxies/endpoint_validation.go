package proxies

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/orchestrator"
	"github.com/neutree-ai/neutree/pkg/storage"
)

func validateEndpointAcceleratorVirtualization(deps *Dependencies) gin.HandlerFunc {
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

		if validationErr := validateEndpointAcceleratorVirtualizationBody(body); validationErr != nil {
			c.JSON(http.StatusBadRequest, validationErr)
			c.Abort()

			return
		}
		if validationErr := validateEndpointAcceleratorVirtualizationClusterReadiness(body, deps); validationErr != nil {
			c.JSON(http.StatusBadRequest, validationErr)
			c.Abort()

			return
		}

		c.Next()
	}
}

func validateEndpointAcceleratorVirtualizationBody(body []byte) *validationError {
	var endpoint v1.Endpoint
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&endpoint); err != nil {
		return invalidEndpointPayloadError(err)
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

func validateEndpointAcceleratorVirtualizationClusterReadiness(body []byte, deps *Dependencies) *validationError {
	if deps == nil || deps.Storage == nil {
		return nil
	}

	var endpoint v1.Endpoint
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&endpoint); err != nil {
		return invalidEndpointPayloadError(err)
	}

	if endpoint.GetDeletionTimestamp() != "" ||
		endpoint.Spec == nil ||
		endpoint.Spec.Resources == nil ||
		!endpoint.Spec.Resources.HasAcceleratorVirtualization() {
		return nil
	}

	if endpoint.Spec.Cluster == "" {
		return &validationError{
			Code:    "10220",
			Message: "endpoint accelerator virtualization requires deploy cluster",
			Hint:    "Set spec.cluster to the target Kubernetes cluster",
		}
	}

	clusters, err := deps.Storage.ListCluster(storage.ListOption{
		Filters: []storage.Filter{
			{Column: "metadata->name", Operator: "eq", Value: strconv.Quote(endpoint.Spec.Cluster)},
			{Column: "metadata->workspace", Operator: "eq", Value: strconv.Quote(endpoint.Metadata.Workspace)},
		},
	})
	if err != nil {
		return &validationError{
			Code:    "10221",
			Message: "failed to validate endpoint accelerator virtualization dependencies",
			Hint:    err.Error(),
		}
	}

	if len(clusters) == 0 {
		return &validationError{
			Code:    "10220",
			Message: "deploy cluster not found for endpoint accelerator virtualization",
			Hint:    fmt.Sprintf("Cluster %s was not found in workspace %s", endpoint.Spec.Cluster, endpoint.Metadata.Workspace),
		}
	}

	if err := orchestrator.ValidateAcceleratorVirtualizationEndpointDependencies(&endpoint, &clusters[0]); err != nil {
		return &validationError{
			Code:    "10221",
			Message: "endpoint accelerator virtualization dependencies are not ready",
			Hint:    err.Error(),
		}
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
