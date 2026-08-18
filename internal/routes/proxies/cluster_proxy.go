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
	"reflect"
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

type clusterValidationOperation string

const (
	clusterValidationCreate     clusterValidationOperation = "create"
	clusterValidationPatch      clusterValidationOperation = "patch"
	clusterValidationSoftDelete clusterValidationOperation = "soft_delete"
)

// ValidationInput contains the original request data shared by Cluster validators.
type ValidationInput[T any] struct {
	Method      string
	Body        []byte
	RawPayload  map[string]json.RawMessage
	Patch       T
	Current     *T
	New         *T
	QueryParams url.Values
	Operation   clusterValidationOperation
}

func validateClusterRequest(s storage.Storage) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method != http.MethodPost && c.Request.Method != http.MethodPatch {
			c.Next()

			return
		}

		input, validationErr := readClusterValidationInput(c)
		if validationErr != nil {
			c.JSON(validationErrStatus(validationErr), validationErr)
			c.Abort()

			return
		}

		if input.Operation == clusterValidationSoftDelete {
			if err := validateClusterSoftDelete(s, input); err != nil {
				writeClusterDeletionError(c, err)

				return
			}

			c.Next()

			return
		}

		if validationErr := prepareClusterValidationInput(s, input); validationErr != nil {
			c.JSON(validationErrStatus(validationErr), validationErr)
			c.Abort()

			return
		}

		if validationErr := validateClusterVersionInput(input); validationErr != nil {
			c.JSON(validationErrStatus(validationErr), validationErr)
			c.Abort()

			return
		}

		if input.Operation == clusterValidationPatch {
			if validationErr := validateClusterVersionUpdateInput(input); validationErr != nil {
				c.JSON(validationErrStatus(validationErr), validationErr)
				c.Abort()

				return
			}

			if validationErr := validateClusterConfigurationUpdateInput(input); validationErr != nil {
				c.JSON(validationErrStatus(validationErr), validationErr)
				c.Abort()

				return
			}
		}

		if validationErr := validateClusterAcceleratorVirtualizationInput(s, input); validationErr != nil {
			c.JSON(validationErrStatus(validationErr), validationErr)
			c.Abort()

			return
		}

		c.Next()
	}
}

func readClusterValidationInput(c *gin.Context) (*ValidationInput[v1.Cluster], *validationError) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return nil, invalidClusterPayloadError(err)
	}

	if err := c.Request.Body.Close(); err != nil {
		return nil, invalidClusterPayloadError(err)
	}

	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	c.Request.ContentLength = int64(len(body))
	c.Request.Header.Set("Content-Length", strconv.Itoa(len(body)))

	input := &ValidationInput[v1.Cluster]{
		Method:      c.Request.Method,
		Body:        body,
		QueryParams: c.Request.URL.Query(),
		Operation:   clusterValidationCreate,
	}
	if c.Request.Method == http.MethodPatch {
		input.Operation = clusterValidationPatch
	}

	if len(bytes.TrimSpace(body)) == 0 {
		return input, nil
	}

	if err := json.Unmarshal(body, &input.RawPayload); err != nil {
		return nil, invalidClusterPayloadError(err)
	}

	if input.Operation == clusterValidationPatch {
		isSoftDelete, err := clusterPatchIsSoftDelete(input.RawPayload)
		if err != nil {
			return nil, invalidClusterPayloadError(err)
		}

		if isSoftDelete {
			input.Operation = clusterValidationSoftDelete
			if metadataRaw, ok := input.RawPayload["metadata"]; ok {
				if err := json.Unmarshal(metadataRaw, &input.Patch.Metadata); err != nil {
					return nil, invalidClusterPayloadError(err)
				}
			}

			return input, nil
		}
	}

	if err := json.Unmarshal(body, &input.Patch); err != nil {
		return nil, invalidClusterPayloadError(err)
	}

	return input, nil
}

func prepareClusterValidationInput(
	s storage.Storage, input *ValidationInput[v1.Cluster],
) *validationError {
	if input.Operation == clusterValidationSoftDelete {
		return nil
	}

	if input.Operation == clusterValidationCreate {
		input.New = &input.Patch

		return nil
	}

	filters := clusterPatchUpdateFilters(input.Patch, input.QueryParams)
	if len(filters) == 0 {
		return clusterPatchValidationResolutionError(
			errors.New("cluster identity is required when patching a cluster"))
	}

	current, err := resolveClusterForPatchUpdate(s, filters)
	if err != nil {
		if errors.Is(err, errClusterStorageUnavailable) {
			return internalServerValidationError()
		}

		return clusterPatchValidationResolutionError(err)
	}

	newCluster, err := buildPostgrestClusterPatchValidationNew(current, input.Body)
	if err != nil {
		return invalidClusterPayloadError(err)
	}

	input.Current = current
	input.New = newCluster

	return nil
}

// buildPostgrestClusterPatchValidationNew constructs the Cluster state that
// PostgREST will persist. A PATCH replaces each supplied top-level column;
// masked credentials are backfilled first because the proxy forwards them.
func buildPostgrestClusterPatchValidationNew(current *v1.Cluster, body []byte) (*v1.Cluster, error) {
	if current == nil {
		return nil, errors.New("current cluster is required")
	}

	currentBody, err := json.Marshal(current)
	if err != nil {
		return nil, fmt.Errorf("marshal current cluster: %w", err)
	}

	var currentPayload map[string]interface{}
	if err := json.Unmarshal(currentBody, &currentPayload); err != nil {
		return nil, fmt.Errorf("decode current cluster: %w", err)
	}

	if len(bytes.TrimSpace(body)) == 0 {
		body = []byte(`{}`)
	}

	filteredBody, err := filterPayloadToTopLevelFields(
		body,
		extractTopLevelJSONFields(reflect.TypeOf(v1.Cluster{})),
	)
	if err != nil {
		return nil, fmt.Errorf("filter cluster patch payload: %w", err)
	}

	var patchPayload map[string]interface{}
	if err := json.Unmarshal(filteredBody, &patchPayload); err != nil {
		return nil, fmt.Errorf("decode cluster patch payload: %w", err)
	}

	tagConfig := extractStructTagConfig(reflect.TypeOf(v1.Cluster{}))
	mergeExcludedFields(patchPayload, currentPayload, tagConfig.excludeFields, tagConfig.arrayMergeKeys)

	for field, value := range patchPayload {
		currentPayload[field] = value
	}

	nextBody, err := json.Marshal(currentPayload)
	if err != nil {
		return nil, fmt.Errorf("marshal patched cluster: %w", err)
	}

	var next v1.Cluster
	if err := json.Unmarshal(nextBody, &next); err != nil {
		return nil, fmt.Errorf("decode patched cluster: %w", err)
	}

	return &next, nil
}

func validateClusterSoftDelete(s storage.Storage, input *ValidationInput[v1.Cluster]) error {
	if input.Patch.Metadata == nil || input.Patch.Metadata.Name == "" {
		return nil
	}

	return validateClusterDeletion(s)(input.Patch.Metadata.Workspace, input.Patch.Metadata.Name)
}

func writeClusterDeletionError(c *gin.Context, err error) {
	deletionErr, ok := err.(*middleware.DeletionError)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    "500",
			"message": fmt.Sprintf("Failed to validate deletion: %v", err),
		})
		c.Abort()

		return
	}

	c.Header("X-Powered-By", "Neutree")
	c.JSON(http.StatusBadRequest, gin.H{
		"code":    deletionErr.Code,
		"message": deletionErr.Message,
		"hint":    deletionErr.Hint,
	})
	c.Abort()
}

func clusterPatchIsSoftDelete(payload map[string]json.RawMessage) (bool, error) {
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

	return parseClusterPatchConfigurationUpdatePayload(payload)
}

func parseClusterPatchConfigurationUpdatePayload(payload map[string]json.RawMessage) (clusterPatchConfigurationUpdate, error) {
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

func validateInitializedClusterConfigurationUpdate(
	current, next *v1.Cluster, update clusterPatchConfigurationUpdate,
) error {
	if current == nil || current.Spec == nil || next == nil || next.Spec == nil || !current.IsInitialized() {
		return nil
	}

	if update.configCleared {
		return errors.New("config cannot be cleared after cluster initialization")
	}

	if update.imageRegistrySet && next.Spec.ImageRegistry != current.Spec.ImageRegistry {
		return errors.New("image registry cannot be changed after cluster initialization")
	}

	if current.Spec.Type == v1.KubernetesClusterType {
		if update.kubernetesConfigCleared {
			return errors.New("kubernetes_config cannot be cleared for an initialized cluster")
		}

		if update.kubeconfigSet && update.kubeconfig != "" {
			currentKubeconfig, err := util.GetKubeConfigFromCluster(current)
			if err != nil {
				return fmt.Errorf("failed to read current kubeconfig: %w", err)
			}

			currentAPIServer, err := util.GetApiServerUrlFromDecodedKubeConfig(currentKubeconfig)
			if err != nil {
				return fmt.Errorf("failed to parse current kubeconfig: %w", err)
			}

			if next.Spec.Config == nil || next.Spec.Config.KubernetesConfig == nil {
				return errors.New("updated Kubernetes config is required")
			}

			updatedKubeconfig := next.Spec.Config.KubernetesConfig.Kubeconfig

			updatedAPIServer, err := util.GetApiServerUrlFromKubeConfig(updatedKubeconfig)
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

func validateClusterVersionInput(input *ValidationInput[v1.Cluster]) *validationError {
	if input.Operation == clusterValidationSoftDelete {
		return nil
	}

	if input.New == nil {
		return clusterPatchValidationResolutionError(errors.New("updated cluster is required"))
	}

	if strings.TrimSpace(input.New.GetVersion()) == "" {
		return &validationError{
			Code:    "10209",
			Message: "spec.version is required",
			Hint:    "Provide a non-empty spec.version",
		}
	}

	if err := validateDesiredClusterVersion(input.New.GetVersion()); err != nil {
		return clusterVersionValidationError(err)
	}

	return nil
}

func validateClusterVersionUpdateInput(input *ValidationInput[v1.Cluster]) *validationError {
	if input.Current == nil || input.New == nil {
		return clusterPatchValidationResolutionError(errors.New("cluster validation objects are required"))
	}

	if input.New.GetVersion() == input.Current.GetVersion() {
		return nil
	}

	if err := validateClusterVersionNotDowngrade(input.Current, input.New.GetVersion()); err != nil {
		return clusterVersionUpdateError(err)
	}

	return nil
}

func validateClusterConfigurationUpdateInput(input *ValidationInput[v1.Cluster]) *validationError {
	configurationUpdate, err := parseClusterPatchConfigurationUpdatePayload(input.RawPayload)
	if err != nil {
		return invalidClusterPayloadError(err)
	}

	if !configurationUpdate.hasChanges() {
		return nil
	}

	if input.Current == nil || input.New == nil {
		return clusterPatchValidationResolutionError(errors.New("cluster validation objects are required"))
	}

	if err := validateInitializedClusterConfigurationUpdate(input.Current, input.New, configurationUpdate); err != nil {
		return clusterConfigurationUpdateError(err)
	}

	return nil
}

func clusterPatchUpdateFilters(patch v1.Cluster, queryParams url.Values) []storage.Filter {
	filters := queryParamsToFilters(queryParams)
	if len(filters) == 0 && patch.Metadata != nil && patch.Metadata.Workspace != "" && patch.Metadata.Name != "" {
		filters = []storage.Filter{
			{Column: "metadata->>workspace", Operator: "eq", Value: patch.Metadata.Workspace},
			{Column: "metadata->>name", Operator: "eq", Value: patch.Metadata.Name},
		}
	}

	return filters
}

// errClusterStorageUnavailable marks storage-layer failures (e.g. a database
// outage) so they surface as internal server error responses instead of
// client validation errors.
var errClusterStorageUnavailable = errors.New("cluster storage unavailable")

func resolveClusterForPatchUpdate(s storage.Storage, filters []storage.Filter) (*v1.Cluster, error) {
	clusters, err := s.ListCluster(storage.ListOption{Filters: filters})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errClusterStorageUnavailable, err)
	}

	if len(clusters) != 1 {
		return nil, fmt.Errorf("expected exactly one cluster from patch filters, got %d", len(clusters))
	}

	current := &clusters[0]
	if current.Spec == nil {
		return nil, errors.New("current cluster spec is required")
	}

	return current, nil
}

func clusterVersionUpdateError(err error) *validationError {
	return &validationError{
		Code:    "10212",
		Message: "invalid cluster version update",
		Hint:    err.Error(),
	}
}

func clusterVersionValidationError(err error) *validationError {
	return &validationError{
		Code:    "10212",
		Message: "invalid cluster version",
		Hint:    err.Error(),
	}
}

func clusterPatchValidationResolutionError(err error) *validationError {
	return &validationError{
		Code:    "10209",
		Message: "failed to prepare cluster patch validation",
		Hint:    err.Error(),
	}
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

	return validateClusterAcceleratorVirtualizationDisableForIdentity(s, workspace, name)
}

func validateClusterAcceleratorVirtualizationDisableForIdentity(
	s storage.Storage, workspace, name string,
) *validationError {
	endpoints, err := s.ListEndpoint(storage.ListOption{Filters: clusterEndpointReferenceFilters(workspace, name)})
	if err != nil {
		return internalServerValidationError()
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

func validateClusterAcceleratorVirtualizationDisableForCurrent(
	s storage.Storage, current *v1.Cluster, patch v1.Cluster,
) *validationError {
	if current == nil || current.Metadata == nil || current.Metadata.Workspace == "" || current.Metadata.Name == "" {
		return &validationError{
			Code:    "10209",
			Message: "failed to validate cluster accelerator virtualization",
			Hint:    "cluster identity is required when disabling accelerator virtualization",
		}
	}

	if patch.Metadata != nil &&
		((patch.Metadata.Workspace != "" && patch.Metadata.Workspace != current.Metadata.Workspace) ||
			(patch.Metadata.Name != "" && patch.Metadata.Name != current.Metadata.Name)) {
		return &validationError{
			Code:    "10209",
			Message: "failed to validate cluster accelerator virtualization",
			Hint:    "cluster metadata in patch body does not match patch target",
		}
	}

	return validateClusterAcceleratorVirtualizationDisableForIdentity(
		s, current.Metadata.Workspace, current.Metadata.Name)
}

// validateClusterAcceleratorVirtualizationModeSwitch blocks switching the
// cluster accelerator virtualization mode while any endpoint still uses
// accelerator virtualization. The gate applies to any mode change (e.g.
// core -> template and template -> core alike) once virtualization is enabled.
// When the mode is unchanged, or virtualization is not enabled on the target
// cluster, the gate is skipped.
func validateClusterAcceleratorVirtualizationModeSwitch(
	s storage.Storage, current, next *v1.Cluster,
) *validationError {
	if next == nil || next.Spec == nil || !next.Spec.AcceleratorVirtualizationEnabled() {
		return nil
	}

	// The dispatch guard only routes already-enabled clusters here, but the
	// function is public and dereferences current below, so keep the contract
	// explicit for any future caller.
	if current == nil || current.Spec == nil || !current.Spec.AcceleratorVirtualizationEnabled() {
		return nil
	}

	if currentSpecMode(current) == next.Spec.AcceleratorVirtualization.Mode {
		return nil
	}

	workspace, name, validationErr := resolveClusterIdentityForAcceleratorVirtualizationDisable(
		s, *current, url.Values{})
	if validationErr != nil {
		return validationErr
	}

	endpoints, err := s.ListEndpoint(storage.ListOption{Filters: clusterEndpointReferenceFilters(workspace, name)})
	if err != nil {
		return &validationError{
			Code:    "10228",
			Message: "failed to validate cluster accelerator virtualization mode switch",
			Hint:    err.Error(),
		}
	}

	count := 0

	for _, endpoint := range endpoints {
		if endpointRequestsVirtualization(&endpoint) {
			count++
		}
	}

	if count > 0 {
		return &validationError{
			Code:    "10228",
			Message: fmt.Sprintf("cannot switch cluster accelerator virtualization mode for cluster '%s/%s'", workspace, name),
			Hint: fmt.Sprintf(
				"%d vGPU endpoint(s) still use accelerator virtualization; disable virtualization on the endpoints before switching",
				count,
			),
		}
	}

	return nil
}

// currentSpecMode returns the virtualization mode currently set on the
// cluster spec, or the empty string when virtualization is not configured.
func currentSpecMode(cluster *v1.Cluster) v1.AcceleratorVirtualizationMode {
	if cluster == nil || cluster.Spec == nil || cluster.Spec.AcceleratorVirtualization == nil {
		return ""
	}

	return cluster.Spec.AcceleratorVirtualization.Mode
}

// endpointRequestsVirtualization reports whether the endpoint has accelerator
// virtualization enabled on any of its resources. Switching a cluster to
// template mode while any vGPU endpoint exists is rejected regardless of the
// exact virtualization parameters requested.
func endpointRequestsVirtualization(endpoint *v1.Endpoint) bool {
	if endpoint == nil || endpoint.Spec == nil || endpoint.Spec.Resources == nil {
		return false
	}

	return endpoint.Spec.Resources.HasAcceleratorVirtualization()
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

func validateClusterAcceleratorVirtualizationInput(
	s storage.Storage, input *ValidationInput[v1.Cluster],
) *validationError {
	cluster := input.Patch
	if input.New != nil {
		cluster = *input.New
	}

	if cluster.Spec != nil && cluster.Spec.Type != "" {
		if validationErr := validateClusterAcceleratorVirtualizationCluster(cluster); validationErr != nil {
			return validationErr
		}
	}

	if input.Operation != clusterValidationPatch {
		return nil
	}

	if input.Current == nil || input.New == nil {
		return nil
	}

	return validateClusterAcceleratorVirtualizationTransition(s, input)
}

// validateClusterAcceleratorVirtualizationTransition classifies the
// accelerator virtualization transition requested by a cluster PATCH and
// dispatches to the guard that owns that transition shape. Classifying first
// keeps each guard focused on one shape: a first-time enable (virtualization
// currently off) always goes to the enable guard even when the patch also sets
// a mode, a disable always goes to the disable guard, and the mode-switch guard
// only ever sees an already-enabled cluster changing modes. A patch that does
// not change the virtualization state runs no guard.
func validateClusterAcceleratorVirtualizationTransition(
	s storage.Storage, input *ValidationInput[v1.Cluster],
) *validationError {
	currentEnabled := input.Current.Spec.AcceleratorVirtualizationEnabled()
	newEnabled := input.New.Spec.AcceleratorVirtualizationEnabled()

	switch {
	case !currentEnabled && newEnabled:
		// Enabling changes the cluster's GPU scheduling and device-plugin
		// behavior. Running inference endpoints would be disrupted by the
		// scheduler/webhook takeover, so the enable transition is gated on the
		// cluster having no running GPU endpoints.
		return validateClusterAcceleratorVirtualizationEnableForCurrent(s, input.Current, input.Patch)

	case currentEnabled && !newEnabled:
		// Disabling removes HAMi while vGPU endpoints still reference it.
		return validateClusterAcceleratorVirtualizationDisableForCurrent(s, input.Current, input.Patch)

	case currentEnabled && newEnabled:
		// Mode-switch guard decides internally whether the mode actually
		// changed; unchanged mode runs no guard.
		return validateClusterAcceleratorVirtualizationModeSwitch(s, input.Current, input.New)

	default:
		// Virtualization stays disabled.
		return nil
	}
}

func validateClusterAcceleratorVirtualizationEnableForCurrent(
	s storage.Storage, current *v1.Cluster, patch v1.Cluster,
) *validationError {
	if current == nil || current.Metadata == nil || current.Metadata.Workspace == "" || current.Metadata.Name == "" {
		return &validationError{
			Code:    "10209",
			Message: "failed to validate cluster accelerator virtualization",
			Hint:    "cluster identity is required when enabling accelerator virtualization",
		}
	}

	if patch.Metadata != nil &&
		((patch.Metadata.Workspace != "" && patch.Metadata.Workspace != current.Metadata.Workspace) ||
			(patch.Metadata.Name != "" && patch.Metadata.Name != current.Metadata.Name)) {
		return &validationError{
			Code:    "10209",
			Message: "failed to validate cluster accelerator virtualization",
			Hint:    "cluster metadata in patch body does not match patch target",
		}
	}

	return validateClusterAcceleratorVirtualizationEnableForIdentity(
		s, current.Metadata.Workspace, current.Metadata.Name)
}

func validateClusterAcceleratorVirtualizationEnableForIdentity(
	s storage.Storage, workspace, name string,
) *validationError {
	endpoints, err := s.ListEndpoint(storage.ListOption{Filters: clusterEndpointReferenceFilters(workspace, name)})
	if err != nil {
		return internalServerValidationError()
	}

	runningGPUEndpointCount := 0

	for _, endpoint := range endpoints {
		if endpointRequestsRunningGPU(&endpoint) {
			runningGPUEndpointCount++
		}
	}

	if runningGPUEndpointCount > 0 {
		return &validationError{
			Code:    "10229",
			Message: fmt.Sprintf("cannot enable accelerator virtualization for cluster '%s/%s'", workspace, name),
			Hint: fmt.Sprintf(
				"%d GPU endpoint(s) still run on this cluster; pause or delete the GPU endpoints before enabling accelerator virtualization",
				runningGPUEndpointCount,
			),
		}
	}

	return nil
}

// endpointRequestsRunningGPU reports whether an endpoint requests accelerator
// resources (GPU) and is not paused. Pausing an endpoint scales its replicas
// to zero, so a paused endpoint has replicas == 0.
func endpointRequestsRunningGPU(endpoint *v1.Endpoint) bool {
	if endpoint == nil || endpoint.Spec == nil || endpoint.Spec.Resources == nil {
		return false
	}

	if !endpoint.Spec.Resources.HasAccelerator() {
		return false
	}

	return endpointReplicaCount(endpoint.Spec) > 0
}

func validateClusterAcceleratorVirtualizationCluster(cluster v1.Cluster) *validationError {
	if cluster.Spec == nil || cluster.Spec.AcceleratorVirtualization == nil {
		return nil
	}

	if !cluster.Spec.AcceleratorVirtualization.Enabled {
		return nil
	}

	if err := clustervalidation.ValidateAcceleratorVirtualizationMode(
		cluster.Spec.AcceleratorVirtualization.Mode); err != nil {
		return acceleratorVirtualizationValidationError(err)
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

	handler := CreateStructProxyHandler[v1.Cluster](deps, storage.CLUSTERS_TABLE)
	validation := validateClusterRequest(deps.Storage)

	proxyGroup.GET("", handler)
	proxyGroup.POST("", validation, handler)
	proxyGroup.PATCH("", validation, handler)
}
