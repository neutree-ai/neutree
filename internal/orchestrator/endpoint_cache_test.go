package orchestrator

import (
	"context"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/clustercache"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCacheDependencyWaitsForOldPods(t *testing.T) {
	s := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(s))
	require.NoError(t, appsv1.AddToScheme(s))
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "ns"}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "old", Namespace: "ns", Labels: map[string]string{"app": "inference", "endpoint": "test", clustercache.EndpointLabel: "true"}}}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(dep, pod).Build()
	e := &v1.Endpoint{Metadata: &v1.Metadata{Name: "test"}, Spec: &v1.EndpointSpec{ZCache: &v1.EndpointZCacheSpec{Enabled: false}}, Status: &v1.EndpointStatus{ZCache: &v1.EndpointZCacheStatus{Generation: 4, InUse: true}}}
	observed, err := observeEndpointCache(c, "ns", e)
	require.NoError(t, err)
	require.True(t, observed.InUse)
	require.EqualValues(t, 4, observed.Generation)
	require.NoError(t, c.Delete(context.Background(), pod))
	observed, err = observeEndpointCache(c, "ns", e)
	require.NoError(t, err)
	require.False(t, observed.InUse)

	zero := int32(0)
	dep.Spec.Replicas = &zero
	dep.Spec.Template.Labels = map[string]string{clustercache.EndpointLabel: "true"}
	require.NoError(t, c.Update(context.Background(), dep))
	observed, err = observeEndpointCache(c, "ns", e)
	require.NoError(t, err)
	require.False(t, observed.InUse, "a paused old template with no Pods no longer uses cache")
}
