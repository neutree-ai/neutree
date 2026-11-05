// Package kvcache configures inference engines to use optional KV cache providers.
package kvcache

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1 "k8s.io/api/apps/v1"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

type Provider interface {
	Validate(*v1.Endpoint, *v1.Cluster) error
	Configure(*v1.Endpoint, *v1.Cluster, *appsv1.Deployment) error
}

type Providers map[string]Provider

func (p Providers) Get(name string) (Provider, error) {
	if provider := p[name]; provider != nil {
		return provider, nil
	}

	if name == "vllm-native" {
		return VLLMNative{}, nil
	}

	return nil, fmt.Errorf("KV cache provider %q is not supported", name)
}

func Validate(endpoint *v1.Endpoint, cluster *v1.Cluster, providers Providers) error {
	if endpoint.Spec.KVCache == nil {
		return nil
	}

	provider, err := providers.Get(endpoint.Spec.KVCache.Provider)

	if err != nil {
		return err
	}

	return provider.Validate(endpoint, cluster)
}

func DecodeOptions(raw json.RawMessage, target interface{}) error {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()

	return decoder.Decode(target)
}

// DeploymentGate controls whether a provider permits creating or updating a
// Deployment. It does not block pausing or deleting an endpoint.
type DeploymentGate interface {
	DeploymentReady(*v1.Endpoint, *v1.Cluster) (bool, error)
}

func (p Providers) DeploymentReady(endpoint *v1.Endpoint, cluster *v1.Cluster) (bool, error) {
	name := ""
	if endpoint.Spec.KVCache != nil {
		name = endpoint.Spec.KVCache.Provider
	} else if endpoint.Status != nil && endpoint.Status.KVCache != nil {
		name = endpoint.Status.KVCache.Provider
	}

	if name == "" {
		return true, nil
	}

	provider, err := p.Get(name)
	if err != nil {
		return false, err
	}

	if gate, ok := provider.(DeploymentGate); ok {
		return gate.DeploymentReady(endpoint, cluster)
	}

	return true, nil
}

// StatusObserver observes provider-owned resources and enriches endpoint status.
// dep is nil when the Deployment does not exist; Pods may still use the provider.
// specPending reports whether the current endpoint spec has yet to be applied.
// Observers must preserve deletion, pause and failure phases. They may still
// update provider observations in those phases. DELETED endpoints are not observed.
type StatusObserver interface {
	ObserveStatus(ctx context.Context, c client.Client, namespace string, endpoint *v1.Endpoint, cluster *v1.Cluster,
		dep *appsv1.Deployment, specPending bool, status *v1.EndpointStatus) error
}

func (p Providers) ObserveStatus(ctx context.Context, c client.Client, namespace string, endpoint *v1.Endpoint, cluster *v1.Cluster,
	dep *appsv1.Deployment, specPending bool, status *v1.EndpointStatus) error {
	if endpoint.Status == nil || endpoint.Status.KVCache == nil {
		return nil
	}

	// Observe the provider that owns the recorded resources, even after the
	// desired configuration disables or replaces it.
	status.KVCache = endpoint.Status.KVCache

	provider, err := p.Get(status.KVCache.Provider)
	if err != nil {
		return err
	}

	if observer, ok := provider.(StatusObserver); ok {
		return observer.ObserveStatus(ctx, c, namespace, endpoint, cluster, dep, specPending, status)
	}

	return nil
}
