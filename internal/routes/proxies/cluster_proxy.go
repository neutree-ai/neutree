package proxies

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator"
	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
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

		if validationErr := validateClusterAcceleratorVirtualizationBody(c.Request.Method, body, s); validationErr != nil {
			c.JSON(http.StatusBadRequest, validationErr)
			c.Abort()

			return
		}

		mutatedBody, err := mutateClusterAcceleratorVirtualizationDefaults(body)
		if err != nil {
			c.JSON(http.StatusBadRequest, invalidClusterPayloadError(err))
			c.Abort()

			return
		}

		c.Request.Body = io.NopCloser(bytes.NewReader(mutatedBody))
		c.Request.ContentLength = int64(len(mutatedBody))
		c.Request.Header.Set("Content-Length", fmt.Sprintf("%d", len(mutatedBody)))

		c.Next()
	}
}

func validateClusterAcceleratorVirtualizationBody(
	method string,
	body []byte,
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

	if err := validateClusterAcceleratorVirtualizationConfigPatch(cluster.Spec.AcceleratorVirtualization.ConfigPatch); err != nil {
		return err
	}

	if method == http.MethodPatch && (cluster.Spec.Type == "" || cluster.Spec.Version == "") {
		// Enabling virtualization through PATCH may only send the changed
		// accelerator_virtualization object. Load immutable cluster attributes
		// from the existing row before validating support.
		existingSpec, err := existingClusterSpec(cluster, s)
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

func mutateClusterAcceleratorVirtualizationDefaults(body []byte) ([]byte, error) {
	var payload map[string]interface{}
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&payload); err != nil {
		return nil, err
	}

	spec, ok := payload["spec"].(map[string]interface{})
	if !ok {
		return body, nil
	}

	acceleratorVirtualization, ok := spec["accelerator_virtualization"].(map[string]interface{})
	if !ok {
		return body, nil
	}

	enabled, ok := acceleratorVirtualization["enabled"].(bool)
	if !ok || !enabled {
		return body, nil
	}

	configPatch, ok := acceleratorVirtualization["config_patch"].(map[string]interface{})
	if !ok {
		configPatch = map[string]interface{}{}
		acceleratorVirtualization["config_patch"] = configPatch
	}

	if policy, ok := nestedStringFromMap(configPatch, "scheduler", "defaultSchedulerPolicy", "gpuSchedulerPolicy"); ok &&
		strings.TrimSpace(policy) != "" {
		return body, nil
	}

	setNestedStringToMap(configPatch, plugin.NvidiaGPUTopologyAwarePolicy,
		"scheduler", "defaultSchedulerPolicy", "gpuSchedulerPolicy")

	return json.Marshal(payload)
}

func validateClusterAcceleratorVirtualizationConfigPatch(configPatch map[string]interface{}) *validationError {
	for key := range configPatch {
		switch key {
		case "devicePlugin", "scheduler", "global":
		default:
			return &validationError{
				Code:    "10210",
				Message: fmt.Sprintf("unsupported accelerator_virtualization.config_patch key %q", key),
				Hint:    "Only devicePlugin, scheduler, and global config_patch keys are supported",
			}
		}
	}

	if schedulerPatch, ok := nestedBoolFromMap(configPatch, "scheduler", "patch", "enabled"); ok && schedulerPatch {
		return &validationError{
			Code:    "10210",
			Message: "HAMi scheduler patch hook is managed by Neutree and cannot be enabled",
			Hint:    "Remove scheduler.patch.enabled from accelerator_virtualization.config_patch",
		}
	}

	if certManager, ok := nestedBoolFromMap(configPatch, "scheduler", "certManager", "enabled"); ok && certManager {
		return &validationError{
			Code:    "10210",
			Message: "HAMi cert-manager integration is managed by Neutree and cannot be enabled",
			Hint:    "Remove scheduler.certManager.enabled from accelerator_virtualization.config_patch",
		}
	}

	if migStrategy, ok := nestedStringFromMap(configPatch, "devicePlugin", "migStrategy"); ok &&
		strings.ToLower(strings.TrimSpace(migStrategy)) != "none" {
		return &validationError{
			Code:    "10210",
			Message: "HAMi MIG virtualization mode is not supported",
			Hint:    "Set devicePlugin.migStrategy to none or remove it from accelerator_virtualization.config_patch",
		}
	}

	return nil
}

func nestedBoolFromMap(values map[string]interface{}, path ...string) (bool, bool) {
	value, ok := nestedValueFromMap(values, path...)
	if !ok {
		return false, false
	}

	boolValue, ok := value.(bool)

	return boolValue, ok
}

func nestedStringFromMap(values map[string]interface{}, path ...string) (string, bool) {
	value, ok := nestedValueFromMap(values, path...)
	if !ok {
		return "", false
	}

	stringValue, ok := value.(string)

	return stringValue, ok
}

func nestedValueFromMap(values map[string]interface{}, path ...string) (interface{}, bool) {
	var current interface{} = values
	for _, key := range path {
		asMap, ok := current.(map[string]interface{})
		if !ok {
			return nil, false
		}

		current, ok = asMap[key]
		if !ok {
			return nil, false
		}
	}

	return current, true
}

func setNestedStringToMap(values map[string]interface{}, value string, path ...string) {
	current := values
	for _, key := range path[:len(path)-1] {
		next, ok := current[key].(map[string]interface{})
		if !ok {
			next = map[string]interface{}{}
			current[key] = next
		}

		current = next
	}

	current[path[len(path)-1]] = value
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

func existingClusterSpec(cluster v1.Cluster, s storage.Storage) (*v1.ClusterSpec, error) {
	if s == nil {
		return nil, fmt.Errorf("include metadata.name, metadata.workspace, spec.type, and spec.version when enabling accelerator virtualization")
	}

	name := cluster.GetName()
	workspace := cluster.GetWorkspace()

	if name == "" || workspace == "" {
		return nil, fmt.Errorf("include metadata.name and metadata.workspace when enabling accelerator virtualization")
	}

	filters := []storage.Filter{
		{Column: "metadata->>name", Operator: "eq", Value: name},
		{Column: "metadata->>workspace", Operator: "eq", Value: workspace},
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
