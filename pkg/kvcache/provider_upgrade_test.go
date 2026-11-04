package kvcache

import (
	"errors"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
)

type upgradeProvider struct {
	VLLMNative
	ready bool
	err   error
	seen  *v1.Endpoint
}

func (p *upgradeProvider) DeploymentReady(ep *v1.Endpoint, _ *v1.Cluster) (bool, error) {
	p.seen = ep
	return p.ready, p.err
}

func (p *upgradeProvider) ObserveDeployment(ep *v1.Endpoint, _ *v1.Cluster, _ *appsv1.Deployment, status *v1.EndpointStatus) error {
	p.seen = ep
	status.ErrorMessage = "observed current deployment"
	return p.err
}

func TestUpgradeProviderSelection(t *testing.T) {
	for _, tc := range []struct {
		name        string
		spec        string
		status      string
		selected    bool
		unsupported bool
	}{
		{name: "no cache"},
		{name: "native provider needs no upgrade hooks", spec: "vllm-native"},
		{name: "configured provider", spec: "shared", selected: true},
		{name: "removed cache still observes deployed provider", status: "shared", selected: true},
		{name: "configured provider wins over previous status", spec: "vllm-native", status: "shared"},
		{name: "unknown configured provider fails closed", spec: "unknown", unsupported: true},
		{name: "unknown deployed provider fails closed", status: "unknown", unsupported: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ep := &v1.Endpoint{Spec: &v1.EndpointSpec{}}
			if tc.spec != "" {
				ep.Spec.KVCache = &v1.EndpointKVCacheSpec{Provider: tc.spec}
			}
			if tc.status != "" {
				ep.Status = &v1.EndpointStatus{KVCache: &v1.EndpointKVCacheStatus{Provider: tc.status}}
			}
			provider := &upgradeProvider{}
			providers := Providers{"shared": provider}
			ready, err := providers.DeploymentReady(ep, nil)
			status := &v1.EndpointStatus{}
			observeErr := providers.ObserveDeployment(ep, nil, &appsv1.Deployment{}, status)
			if tc.unsupported {
				require.ErrorContains(t, err, "not supported")
				require.ErrorContains(t, observeErr, "not supported")
				require.False(t, ready)
				return
			}
			require.NoError(t, err)
			require.NoError(t, observeErr)
			require.Equal(t, !tc.selected, ready)
			if tc.selected {
				require.Same(t, ep, provider.seen)
				require.Equal(t, "observed current deployment", status.ErrorMessage)
			} else {
				require.Nil(t, provider.seen)
				require.Empty(t, status.ErrorMessage)
			}
		})
	}
}

func TestUpgradeProviderPropagatesFailure(t *testing.T) {
	failure := errors.New("cache observation unavailable")
	provider := &upgradeProvider{err: failure}
	providers := Providers{"shared": provider}
	ep := &v1.Endpoint{Spec: &v1.EndpointSpec{KVCache: &v1.EndpointKVCacheSpec{Provider: "shared"}}}
	ready, err := providers.DeploymentReady(ep, nil)
	require.False(t, ready)
	require.ErrorIs(t, err, failure)
	require.ErrorIs(t, providers.ObserveDeployment(ep, nil, nil, &v1.EndpointStatus{}), failure)
	provider.err = nil
	provider.ready = true
	ready, err = providers.DeploymentReady(ep, nil)
	require.True(t, ready)
	require.NoError(t, err)
}
