// Package kvcache configures inference engines to use optional KV cache providers.
package kvcache

import (
	"bytes"
	"encoding/json"
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
	appsv1 "k8s.io/api/apps/v1"
)

const ZCacheDependencyLabel = "neutree.ai/uses-cluster-cache"

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
		return Native{}, nil
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
