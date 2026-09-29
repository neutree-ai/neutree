package cluster

import (
	"context"
	"encoding/base64"
	"errors"
	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/clustercache"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestCommunityCacheIsAbsentByDefault(t *testing.T) {
	cluster := &v1.Cluster{Spec: &v1.ClusterSpec{}, Status: &v1.ClusterStatus{Phase: v1.ClusterPhaseRunning}}
	r := &NativeKubernetesClusterReconciler{}
	r.reconcileCache(&ReconcileContext{Cluster: cluster})
	require.Nil(t, cluster.Status.ZCache)
	require.NoError(t, r.deleteCache(&ReconcileContext{Cluster: cluster}))
	cluster.Spec.ZCache = &v1.ZCacheSpec{Enabled: true}
	r.reconcileCache(&ReconcileContext{Cluster: cluster})
	require.Equal(t, v1.ClusterPhaseRunning, cluster.Status.Phase)
	require.Equal(t, "Failed", cluster.Status.ZCache.Phase)
	require.Contains(t, cluster.Status.ZCache.Message, "not supported")
	// An unsupported distribution must not silently orphan an existing cache.
	require.ErrorContains(t, r.deleteCache(&ReconcileContext{Cluster: cluster}), "cleanup requires")
}

type failingCacheProvider struct{ calls int }

func (p *failingCacheProvider) Reconcile(_ context.Context, rc *clustercache.Context) error {
	rc.Cluster.Status.ZCache = &v1.ZCacheStatus{Phase: "Failed", Message: "cache unavailable"}
	p.calls++
	return errors.New("cache unavailable")
}
func (p *failingCacheProvider) Delete(context.Context, *clustercache.Context) error {
	p.calls++
	return errors.New("cleanup pending")
}

func TestProviderFailurePreservesClusterHealthAndBlocksCleanup(t *testing.T) {
	kubeconfig := "apiVersion: v1\nkind: Config\nclusters:\n- name: test\n  cluster:\n    server: https://127.0.0.1:6443\ncontexts:\n- name: test\n  context:\n    cluster: test\ncurrent-context: test\n"
	cl := &v1.Cluster{Metadata: &v1.Metadata{Name: "compute", Workspace: "default"}, Spec: &v1.ClusterSpec{Config: &v1.ClusterConfig{KubernetesConfig: &v1.KubernetesClusterConfig{Kubeconfig: base64.StdEncoding.EncodeToString([]byte(kubeconfig))}}}, Status: &v1.ClusterStatus{Phase: v1.ClusterPhaseRunning}}
	rc := &ReconcileContext{Ctx: context.Background(), Cluster: cl, ImageRegistry: &v1.ImageRegistry{Spec: &v1.ImageRegistrySpec{URL: "https://registry.example"}}}
	provider := &failingCacheProvider{}
	r := &NativeKubernetesClusterReconciler{cacheProvider: provider}
	r.reconcileCache(rc)
	require.Equal(t, 1, provider.calls)
	require.Equal(t, v1.ClusterPhaseRunning, cl.Status.Phase)
	require.Equal(t, "cache unavailable", cl.Status.ZCache.Message)
	require.ErrorContains(t, r.deleteCache(rc), "cleanup pending")
	require.Equal(t, 2, provider.calls)
}
