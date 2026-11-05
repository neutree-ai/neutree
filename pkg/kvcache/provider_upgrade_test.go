package kvcache

import (
	"context"
	"errors"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type upgradeProvider struct {
	VLLMNative
	ready bool
	err   error
}

func (p *upgradeProvider) DeploymentReady(*v1.Endpoint, *v1.Cluster) (bool, error) {
	return p.ready, p.err
}
func (p *upgradeProvider) ObserveStatus(_ context.Context, _ client.Client, _ string, _ *v1.Endpoint, _ *v1.Cluster, _ *appsv1.Deployment, _ bool, status *v1.EndpointStatus) error {
	status.ErrorMessage = "observed deployed provider"
	return p.err
}
func TestUpgradeProviderSelection(t *testing.T) {
	for _, tc := range []struct {
		name, spec, status                       string
		gated, observed, gateError, observeError bool
	}{
		{name: "no cache"},
		{name: "native provider needs no hooks", spec: "vllm-native", status: "vllm-native"},
		{name: "configured shared provider", spec: "shared", status: "shared", gated: true, observed: true},
		{name: "disabled cache still tracks old resources", status: "shared", gated: true, observed: true},
		{name: "replacement spec does not hide old resources", spec: "vllm-native", status: "shared", observed: true},
		{name: "no deployed state yet", spec: "shared", gated: true},
		{name: "unknown configured provider", spec: "unknown", gateError: true},
		{name: "unknown deployed provider", status: "unknown", gateError: true, observeError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ep := &v1.Endpoint{Spec: &v1.EndpointSpec{}}
			if tc.spec != "" {
				ep.Spec.KVCache = &v1.EndpointKVCacheSpec{Provider: tc.spec}
			}
			if tc.status != "" {
				ep.Status = &v1.EndpointStatus{KVCache: &v1.EndpointKVCacheStatus{Provider: tc.status}}
			}
			providers := Providers{"shared": &upgradeProvider{}}
			ready, err := providers.DeploymentReady(ep, nil)
			if tc.gateError {
				require.ErrorContains(t, err, "not supported")
			} else {
				require.NoError(t, err)
				require.Equal(t, !tc.gated, ready)
			}
			status := &v1.EndpointStatus{}
			err = providers.ObserveStatus(context.Background(), nil, "ns", ep, nil, nil, false, status)
			if tc.observeError {
				require.ErrorContains(t, err, "not supported")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.observed, status.ErrorMessage != "")
		})
	}
}
func TestUpgradeProviderPropagatesFailure(t *testing.T) {
	failure := errors.New("cache observation unavailable")
	provider := &upgradeProvider{err: failure}
	providers := Providers{"shared": provider}
	ep := &v1.Endpoint{Spec: &v1.EndpointSpec{KVCache: &v1.EndpointKVCacheSpec{Provider: "shared"}}, Status: &v1.EndpointStatus{KVCache: &v1.EndpointKVCacheStatus{Provider: "shared"}}}
	ready, err := providers.DeploymentReady(ep, nil)
	require.False(t, ready)
	require.ErrorIs(t, err, failure)
	require.ErrorIs(t, providers.ObserveStatus(context.Background(), nil, "ns", ep, nil, nil, false, &v1.EndpointStatus{}), failure)
	provider.err = nil
	provider.ready = true
	ready, err = providers.DeploymentReady(ep, nil)
	require.True(t, ready)
	require.NoError(t, err)
}
