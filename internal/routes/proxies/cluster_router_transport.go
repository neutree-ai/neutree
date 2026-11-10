package proxies

import (
	"fmt"
	"math"

	v1 "github.com/neutree-ai/neutree/api/v1"
	clusterversion "github.com/neutree-ai/neutree/internal/cluster/version"
)

func validateClusterRouterProxyInput(input *ValidationInput[v1.Cluster]) *validationError {
	if input.Operation == clusterValidationPatch {
		if _, hasSpec := input.RawPayload["spec"]; !hasSpec {
			return nil
		}
	}

	spec := input.New.Spec
	if spec == nil || spec.Type != v1.KubernetesClusterType || spec.Config == nil || spec.Config.KubernetesConfig == nil {
		return nil
	}

	router := spec.Config.KubernetesConfig.Router
	if router.MaxConnectionsPerBackend == nil && router.ConnectTimeoutSeconds == nil &&
		router.SockConnectTimeoutSeconds == nil && router.SockReadTimeoutSeconds == nil {
		return nil
	}

	version := spec.Version
	versionField := "spec.version"

	if router.Version != "" {
		version = router.Version
		versionField = "spec.config.kubernetes_config.router.version"
	}

	supported, err := clusterversion.SupportsRouterClientConfig(version)
	if err != nil {
		return clusterRouterProxyError(fmt.Sprintf("%s must be a valid Router version: %v", versionField, err))
	}

	if !supported {
		return clusterRouterProxyError(fmt.Sprintf("Router proxy options require a Router version greater than %s; effective version is %s",
			clusterversion.RouterClientConfigVersionGate, version))
	}

	if router.MaxConnectionsPerBackend != nil && (*router.MaxConnectionsPerBackend < 0 || *router.MaxConnectionsPerBackend > 65535) {
		return clusterRouterProxyError("spec.config.kubernetes_config.router.max_connections_per_backend must be between 0 and 65535")
	}

	for _, timeout := range []struct {
		name  string
		value *float64
	}{
		{name: "connect_timeout_seconds", value: router.ConnectTimeoutSeconds},
		{name: "sock_connect_timeout_seconds", value: router.SockConnectTimeoutSeconds},
		{name: "sock_read_timeout_seconds", value: router.SockReadTimeoutSeconds},
	} {
		if timeout.value != nil && (math.IsNaN(*timeout.value) || math.IsInf(*timeout.value, 0) || *timeout.value < 0 || *timeout.value > 604800) {
			return clusterRouterProxyError("spec.config.kubernetes_config.router." + timeout.name + " must be a finite number between 0 and 604800 seconds")
		}
	}

	return nil
}

func clusterRouterProxyError(hint string) *validationError {
	return &validationError{
		Code:    "10234",
		Message: "invalid Router proxy configuration",
		Hint:    hint,
	}
}
