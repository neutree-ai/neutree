package clustercache

import (
	"fmt"
	"math"

	appsv1 "k8s.io/api/apps/v1"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

const EndpointLabel = "neutree.ai/uses-cluster-cache"
const DefaultTimeoutSeconds = 5.0

// EndpointProvider is an optional distribution adapter. It never mutates the
// persisted Endpoint spec or owns the normal inference Deployment lifecycle.
type EndpointProvider interface {
	Validate(endpoint *v1.Endpoint, cluster *v1.Cluster) error
	Configure(endpoint *v1.Endpoint, cluster *v1.Cluster, deployment, existing *appsv1.Deployment) error
}

func Enabled(endpoint *v1.Endpoint) bool {
	return endpoint != nil && endpoint.Spec != nil && endpoint.Spec.ZCache != nil && endpoint.Spec.ZCache.Enabled
}

func TimeoutSeconds(spec *v1.EndpointZCacheSpec) float64 {
	if spec != nil && spec.TimeoutSeconds != nil {
		return *spec.TimeoutSeconds
	}

	return DefaultTimeoutSeconds
}

func Validate(endpoint *v1.Endpoint, cluster *v1.Cluster, provider EndpointProvider) error {
	if endpoint.Spec.ZCache == nil {
		return nil
	}

	timeout := TimeoutSeconds(endpoint.Spec.ZCache)
	if math.IsNaN(timeout) || math.IsInf(timeout, 0) || timeout < 0.1 || timeout > 60 {
		return fmt.Errorf("cache timeout must be between 0.1 and 60 seconds")
	}

	if !Enabled(endpoint) {
		return nil
	}

	if provider == nil {
		return fmt.Errorf("inference cache is not supported by this distribution")
	}

	return provider.Validate(endpoint, cluster)
}
