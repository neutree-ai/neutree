package clustercache

import (
	"context"
	"fmt"
	"sync"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/require"
)

type testProvider struct {
	validate func(*v1.Cluster, *v1.Cluster) error
}

func (p *testProvider) Validate(old, next *v1.Cluster) error {
	if p.validate != nil {
		return p.validate(old, next)
	}
	return nil
}
func (*testProvider) Reconcile(context.Context, *Context) error { return nil }
func (*testProvider) Delete(context.Context, *Context) error    { return nil }

func TestRegistryValidation(t *testing.T) {
	var registry Providers
	var typedNil *testProvider
	for _, tc := range []struct {
		name     string
		provider Provider
	}{
		{"", &testProvider{}}, {" ", &testProvider{}}, {"nil", nil}, {"typed-nil", typedNil},
	} {
		require.Error(t, registry.Register(tc.name, tc.provider))
	}
	require.Empty(t, registry.Names())
	first := &testProvider{}
	require.NoError(t, registry.Register("b", first))
	require.NoError(t, registry.Register("a", &testProvider{}))
	require.ErrorContains(t, registry.Register("b", &testProvider{}), "already registered")
	got, err := registry.Get("b")
	require.NoError(t, err)
	require.Same(t, first, got)
	require.Equal(t, []string{"a", "b"}, registry.Names())
	names := registry.Names()
	names[0] = "changed"
	require.Equal(t, []string{"a", "b"}, registry.Names())
	_, err = registry.Get("missing")
	require.ErrorContains(t, err, "not supported")
}

func TestAbsentRegistry(t *testing.T) {
	var registry *Providers
	require.Equal(t, []string{}, registry.Names())
	require.NoError(t, registry.Validate(nil, &v1.Cluster{}))
	_, err := registry.Get("missing")
	require.ErrorContains(t, err, "not supported")
}

func TestRegistryConcurrentAccess(t *testing.T) {
	var registry Providers
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprint(i)
			if err := registry.Register(name, &testProvider{}); err != nil {
				t.Error(err)
				return
			}
			if _, err := registry.Get(name); err != nil {
				t.Error(err)
			}
			registry.Names()
		}(i)
	}
	wg.Wait()
	require.Len(t, registry.Names(), 32)
}

func TestValidateDispatchDoesNotHoldRegistryLock(t *testing.T) {
	var registry Providers
	old := &v1.Cluster{Status: &v1.ClusterStatus{Cache: &v1.ClusterCacheStatus{Provider: "a"}}}
	next := &v1.Cluster{}
	require.NoError(t, registry.Register("a", &testProvider{validate: func(gotOld, gotNext *v1.Cluster) error {
		require.Same(t, old, gotOld)
		require.Same(t, next, gotNext)
		return registry.Register("b", &testProvider{})
	}}))
	// Cleanup still dispatches to the installed provider after desired configuration is removed.
	require.NoError(t, registry.Validate(old, next))
	next.Spec = &v1.ClusterSpec{Cache: &v1.ClusterCacheSpec{Provider: "b"}}
	require.ErrorContains(t, registry.Validate(old, next), "cleanup")
}
