package metrics

import (
	"context"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/clustercache"
	"github.com/stretchr/testify/require"
)

type lifecycleOnlyCacheProvider struct{}

func (lifecycleOnlyCacheProvider) Validate(_, _ *v1.Cluster) error { return nil }
func (lifecycleOnlyCacheProvider) Reconcile(context.Context, *clustercache.Context) error {
	return nil
}
func (lifecycleOnlyCacheProvider) Delete(context.Context, *clustercache.Context) error {
	return nil
}

type metricsCacheProvider struct {
	lifecycleOnlyCacheProvider
	t *testing.T
}

func (p metricsCacheProvider) MetricsScrapeConfig(cluster *v1.Cluster, namespace string) string {
	require.Equal(p.t, "tenant", cluster.Metadata.Workspace)
	require.Equal(p.t, "cluster", cluster.Metadata.Name)
	require.Equal(p.t, "cluster-ns", namespace)
	return "- job_name: example-cache\n  static_configs:\n  - targets: ['cache:9000']\n"
}

func TestCacheProviderScrapeInjection(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider clustercache.Provider
		wantJob  bool
	}{
		{name: "community without provider"},
		{name: "lifecycle-only provider", provider: lifecycleOnlyCacheProvider{}},
		{name: "provider metrics", provider: metricsCacheProvider{t: t}, wantJob: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			providers := &clustercache.Providers{}
			if tc.provider != nil {
				require.NoError(t, providers.Register("example", tc.provider))
			}
			component := (&MetricsComponent{
				cluster: &v1.Cluster{
					Metadata: &v1.Metadata{Name: "cluster", Workspace: "tenant"},
					Spec:     &v1.ClusterSpec{Version: "v1.1.2"},
				},
				namespace:             "cluster-ns",
				metricsRemoteWriteURL: "https://metrics.example.com/api/v1/write",
			}).WithCacheProviders(providers)
			objects, err := component.GetMetricsResources(context.Background())
			require.NoError(t, err)
			config := findMetricsConfigMap(t, objects, "vmagent-config").Data["prometheus.yml"]
			assertValidPrometheusYAML(t, config)
			require.Contains(t, config, "neutree-inference")
			if tc.wantJob {
				require.Contains(t, config, "job_name: example-cache")
			} else {
				require.NotContains(t, config, "example-cache")
			}
		})
	}
}
