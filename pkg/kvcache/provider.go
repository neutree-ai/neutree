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

// DependencyObserver tracks provider-owned resources still used by deployed Pods.
type DependencyObserver interface {
	ObserveDependencies(context.Context, client.Client, string, *v1.Endpoint) (*v1.EndpointKVCacheStatus, error)
}

func (p Providers) ObserveDependencies(ctx context.Context, c client.Client, namespace string, endpoint *v1.Endpoint) (*v1.EndpointKVCacheStatus, error) {
	state := endpoint.Status.KVCache
	provider, err := p.Get(state.Provider)

	if err != nil {
		return nil, err
	}

	observer, ok := provider.(DependencyObserver)
	if !ok {
		return state, nil
	}

	return observer.ObserveDependencies(ctx, c, namespace, endpoint)
}
