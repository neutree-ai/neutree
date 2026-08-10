package proxies

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	mastermindssemver "github.com/Masterminds/semver/v3"
	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	clustervalidation "github.com/neutree-ai/neutree/internal/cluster/validation"
	"github.com/neutree-ai/neutree/internal/middleware"
	"github.com/neutree-ai/neutree/internal/semver"
	"github.com/neutree-ai/neutree/internal/util"
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

		if c.Request.Method == http.MethodPatch {
			isSoftDelete, err := clusterPatchIsSoftDelete(body)
			if err != nil {
				c.JSON(http.StatusBadRequest, invalidClusterPayloadError(err))
				c.Abort()

				return
			}

			if isSoftDelete {
				c.Next()
				return
			}
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

		c.Request.Body.Close()
		c.Request.Body = io.NopCloser(bytes.NewReader(body))
		c.Request.ContentLength = int64(len(body))
		c.Request.Header.Set("Content-Length", strconv.Itoa(len(body)))

		if len(bytes.TrimSpace(body)) == 0 {
			c.Next()
			return
		}

		isSoftDelete, err := clusterPatchIsSoftDelete(body)
		if err != nil {
			c.JSON(http.StatusBadRequest, invalidClusterPayloadError(err))
			c.Abort()

			return
		}

		if isSoftDelete {
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

		current, validationErr := resolveClusterForVersionUpdate(s, patch, c.Request.URL.Query())
		if validationErr != nil {
			c.JSON(http.StatusBadRequest, validationErr)
			c.Abort()

			return
		}

		if err := validateClusterVersionNotDowngrade(current, desiredVersion); err != nil {
			c.JSON(http.StatusBadRequest, &validationError{
				Code:    "10212",
				Message: "invalid cluster version update",
				Hint:    err.Error(),
			})
			c.Abort()

			return
		}

		c.Next()
	}
}

func validateClusterConfigurationUpdate(s storage.Storage) gin.HandlerFunc {
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

		c.Request.Body.Close()
		c.Request.Body = io.NopCloser(bytes.NewReader(body))
		c.Request.ContentLength = int64(len(body))
		c.Request.Header.Set("Content-Length", strconv.Itoa(len(body)))

		if len(bytes.TrimSpace(body)) == 0 {
			c.Next()
			return
		}

		isSoftDelete, err := clusterPatchIsSoftDelete(body)
		if err != nil {
			c.JSON(http.StatusBadRequest, invalidClusterPayloadError(err))
			c.Abort()

			return
		}

		if isSoftDelete {
			c.Next()
			return
		}

		configurationUpdate, err := parseClusterPatchConfigurationUpdate(body)
		if err != nil {
			c.JSON(http.StatusBadRequest, invalidClusterPayloadError(err))
			c.Abort()

			return
		}

		if !configurationUpdate.hasChanges() {
			c.Next()
			return
		}

		var patch v1.Cluster
		if err := json.Unmarshal(body, &patch); err != nil {
			c.JSON(http.StatusBadRequest, invalidClusterPayloadError(err))
			c.Abort()

			return
		}

		current, validationErr := resolveClusterForConfigurationUpdate(s, patch, c.Request.URL.Query())
		if validationErr != nil {
			c.JSON(http.StatusBadRequest, validationErr)
			c.Abort()

			return
		}

		if err := validateInitializedClusterConfigurationUpdate(current, configurationUpdate); err != nil {
			c.JSON(http.StatusBadRequest, clusterConfigurationUpdateError(err))
			c.Abort()

			return
		}

		c.Next()
	}
}

func clusterPatchIsSoftDelete(body []byte) (bool, error) {
	var payload struct {
		Metadata *v1.Metadata `json:"metadata"`
	}

	if err := json.Unmarshal(body, &payload); err != nil {
		return false, err
	}

	return payload.Metadata != nil && payload.Metadata.DeletionTimestamp != "", nil
}

type clusterPatchConfigurationUpdate struct {
	imageRegistry           string
	imageRegistrySet        bool
	configCleared           bool
	kubernetesConfigCleared bool
	kubeconfig              string
	kubeconfigSet           bool
	sshConfigCleared        bool
	sshAuthCleared          bool
	sshPrivateKey           string
	sshPrivateKeySet        bool
}

func (u clusterPatchConfigurationUpdate) hasChanges() bool {
	return u.imageRegistrySet ||
		u.configCleared ||
		u.kubernetesConfigCleared ||
		u.kubeconfigSet ||
		u.sshConfigCleared ||
		u.sshAuthCleared ||
		u.sshPrivateKeySet
}

func parseClusterPatchConfigurationUpdate(body []byte) (clusterPatchConfigurationUpdate, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return clusterPatchConfigurationUpdate{}, err
	}

	specRaw, ok := payload["spec"]
	if !ok {
		return clusterPatchConfigurationUpdate{}, nil
	}

	var spec map[string]json.RawMessage
	if err := json.Unmarshal(specRaw, &spec); err != nil {
		return clusterPatchConfigurationUpdate{}, err
	}

	update := clusterPatchConfigurationUpdate{}
	if imageRegistryRaw, ok := spec["image_registry"]; ok {
		if err := json.Unmarshal(imageRegistryRaw, &update.imageRegistry); err != nil {
			return clusterPatchConfigurationUpdate{}, err
		}

		update.imageRegistrySet = true
	}

	configRaw, ok := spec["config"]
	if !ok {
		return update, nil
	}

	if isJSONNull(configRaw) {
		update.configCleared = true
		return update, nil
	}

	var config map[string]json.RawMessage
	if err := json.Unmarshal(configRaw, &config); err != nil {
		return clusterPatchConfigurationUpdate{}, err
	}

	if kubernetesConfigRaw, ok := config["kubernetes_config"]; ok {
		if isJSONNull(kubernetesConfigRaw) {
			update.kubernetesConfigCleared = true
		} else {
			var kubernetesConfig map[string]json.RawMessage
			if err := json.Unmarshal(kubernetesConfigRaw, &kubernetesConfig); err != nil {
				return clusterPatchConfigurationUpdate{}, err
			}

			if kubeconfigRaw, ok := kubernetesConfig["kubeconfig"]; ok {
				if err := json.Unmarshal(kubeconfigRaw, &update.kubeconfig); err != nil {
					return clusterPatchConfigurationUpdate{}, err
				}

				update.kubeconfigSet = true
			}
		}
	}

	if sshConfigRaw, ok := config["ssh_config"]; ok {
		if isJSONNull(sshConfigRaw) {
			update.sshConfigCleared = true
		} else {
			var sshConfig map[string]json.RawMessage
			if err := json.Unmarshal(sshConfigRaw, &sshConfig); err != nil {
				return clusterPatchConfigurationUpdate{}, err
			}

			authRaw, ok := sshConfig["auth"]
			if !ok {
				return update, nil
			}

			if isJSONNull(authRaw) {
				update.sshAuthCleared = true
				return update, nil
			}

			var auth map[string]json.RawMessage
			if err := json.Unmarshal(authRaw, &auth); err != nil {
				return clusterPatchConfigurationUpdate{}, err
			}

			if sshPrivateKeyRaw, ok := auth["ssh_private_key"]; ok {
				if err := json.Unmarshal(sshPrivateKeyRaw, &update.sshPrivateKey); err != nil {
					return clusterPatchConfigurationUpdate{}, err
				}

				update.sshPrivateKeySet = true
			}
		}
	}

	return update, nil
}

func validateInitializedClusterConfigurationUpdate(current *v1.Cluster, update clusterPatchConfigurationUpdate) error {
	if current == nil || current.Spec == nil || !current.IsInitialized() {
		return nil
	}

	if update.configCleared {
		return errors.New("config cannot be cleared after cluster initialization")
	}

	if update.imageRegistrySet && update.imageRegistry != current.Spec.ImageRegistry {
		return errors.New("image registry cannot be changed after cluster initialization")
	}

	if current.Spec.Type == v1.KubernetesClusterType {
		if update.kubernetesConfigCleared {
			return errors.New("kubernetes_config cannot be cleared for an initialized cluster")
		}

		if update.kubeconfigSet {
			if strings.TrimSpace(update.kubeconfig) == "" {
				return errors.New("kubeconfig cannot be empty for an initialized cluster")
			}

			currentKubeconfig, err := util.GetKubeConfigFromCluster(current)
			if err != nil {
				return fmt.Errorf("failed to read current kubeconfig: %w", err)
			}

			currentAPIServer, err := util.GetApiServerUrlFromDecodedKubeConfig(currentKubeconfig)
			if err != nil {
				return fmt.Errorf("failed to parse current kubeconfig: %w", err)
			}

			updatedAPIServer, err := util.GetApiServerUrlFromKubeConfig(update.kubeconfig)
			if err != nil {
				return fmt.Errorf("failed to parse updated kubeconfig: %w", err)
			}

			if currentAPIServer != updatedAPIServer {
				return errors.New("kubeconfig must reference the current Kubernetes API server")
			}
		}
	}

	if current.Spec.Type == v1.SSHClusterType {
		if update.sshConfigCleared {
			return errors.New("ssh_config cannot be cleared for an initialized cluster")
		}

		if update.sshAuthCleared {
			return errors.New("SSH auth cannot be cleared for an initialized cluster")
		}

		if update.sshPrivateKeySet {
			if strings.TrimSpace(update.sshPrivateKey) == "" {
				return errors.New("SSH private key cannot be empty for an initialized cluster")
			}

			if _, err := base64.StdEncoding.DecodeString(update.sshPrivateKey); err != nil {
				return fmt.Errorf("SSH private key must be base64 encoded: %w", err)
			}
		}
	}

	return nil
}

func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func clusterConfigurationUpdateError(err error) *validationError {
	return &validationError{
		Code:    "10209",
		Message: "invalid cluster configuration update",
		Hint:    err.Error(),
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
	return resolveClusterForPatchUpdate(
		s,
		patch,
		queryParams,
		"10212",
		"failed to validate cluster version update",
		"cluster identity is required when updating spec.version",
	)
}

func resolveClusterForConfigurationUpdate(
	s storage.Storage, patch v1.Cluster, queryParams url.Values,
) (*v1.Cluster, *validationError) {
	return resolveClusterForPatchUpdate(
		s,
		patch,
		queryParams,
		"10209",
		"failed to validate cluster configuration update",
		"cluster identity is required when updating cluster configuration",
	)
}

func resolveClusterForPatchUpdate(
	s storage.Storage, patch v1.Cluster, queryParams url.Values, code, message, identityHint string,
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
			Code:    code,
			Message: message,
			Hint:    identityHint,
		}
	}

	clusters, err := s.ListCluster(storage.ListOption{Filters: filters})
	if err != nil {
		return nil, &validationError{
			Code:    code,
			Message: message,
			Hint:    err.Error(),
		}
	}

	if len(clusters) != 1 {
		return nil, &validationError{
			Code:    code,
			Message: message,
			Hint:    fmt.Sprintf("expected exactly one cluster from patch filters, got %d", len(clusters)),
		}
	}

	current := &clusters[0]
	if current.Spec == nil {
		return nil, &validationError{
			Code:    code,
			Message: message,
			Hint:    "current cluster spec is required",
		}
	}

	return current, nil
}

func validateClusterVersionNotDowngrade(current *v1.Cluster, desiredVersion string) error {
	baseline, err := currentClusterSpecVersionBaseline(current)
	if err != nil {
		return err
	}

	if baseline == "" {
		if err := validateDesiredClusterVersion(desiredVersion); err != nil {
			return err
		}

		return nil
	}

	desiredIsOlder, err := semver.LessThan(desiredVersion, baseline)
	if err != nil {
		return fmt.Errorf("invalid desired cluster version %q: %w", desiredVersion, err)
	}

	if desiredIsOlder {
		return fmt.Errorf(
			"cluster version downgrade is not supported: current version is %s, desired version is %s",
			baseline,
			desiredVersion,
		)
	}

	return nil
}

func validateDesiredClusterVersion(desiredVersion string) error {
	if _, err := mastermindssemver.NewVersion(desiredVersion); err != nil {
		return fmt.Errorf("invalid desired cluster version %q: %w", desiredVersion, err)
	}

	return nil
}

func currentClusterSpecVersionBaseline(current *v1.Cluster) (string, error) {
	if current == nil || current.Spec == nil {
		return "", fmt.Errorf("current cluster spec is required")
	}

	baseline := current.GetVersion()
	if baseline == "" {
		return "", nil
	}

	if _, err := mastermindssemver.NewVersion(baseline); err != nil {
		return "", nil
	}

	return baseline, nil
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
	configurationUpdateValidation := validateClusterConfigurationUpdate(deps.Storage)

	proxyGroup.GET("", handler)
	proxyGroup.POST("", acceleratorVirtualizationValidation, handler)
	proxyGroup.PATCH("", deletionValidation, versionUpdateValidation, configurationUpdateValidation, acceleratorVirtualizationValidation, handler)
}
