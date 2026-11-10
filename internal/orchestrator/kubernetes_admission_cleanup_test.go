package orchestrator

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
)

func TestCleanupUnexpectedAdmissionPods(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*corev1.Pod)
		remove bool
	}{
		{name: "owned admission failure", remove: true},
		{name: "running Pod", mutate: func(pod *corev1.Pod) {
			pod.Status = corev1.PodStatus{Phase: corev1.PodRunning}
		}},
		{name: "pending Pod", mutate: func(pod *corev1.Pod) {
			pod.Status = corev1.PodStatus{Phase: corev1.PodPending}
		}},
		{name: "other failure reason", mutate: func(pod *corev1.Pod) {
			pod.Status.Reason = "Evicted"
		}},
		{name: "another namespace", mutate: func(pod *corev1.Pod) {
			pod.Namespace = "another-namespace"
		}},
		{name: "another Deployment", mutate: func(pod *corev1.Pod) {
			pod.Labels["endpoint"] = "other-endpoint"
		}},
		{name: "external Pod", mutate: func(pod *corev1.Pod) {
			pod.Labels = nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, deployment, pod := admissionCleanupFixtures()
			if tc.mutate != nil {
				tc.mutate(pod)
			}
			ctx.ctrClient = admissionCleanupClient(t, deployment, pod)
			cleanupUnexpectedAdmissionPods(ctx)
			err := ctx.ctrClient.Get(context.Background(), client.ObjectKeyFromObject(pod), &corev1.Pod{})
			if tc.remove {
				assert.True(t, apierrors.IsNotFound(err))
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestCleanupUnexpectedAdmissionPodsDeleteFailures(t *testing.T) {
	for _, tc := range []struct {
		name    string
		replace bool
	}{
		{name: "API delete failure does not stop other Pods"},
		{name: "replacement Pod is protected by the UID precondition", replace: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, deployment, pod := admissionCleanupFixtures()
			other := pod.DeepCopy()
			other.Name = "second-failed-pod"
			other.UID = "second-pod-uid"
			base := admissionCleanupClient(t, deployment, pod, other)
			ctx.ctrClient = interceptor.NewClient(base, interceptor.Funcs{
				Delete: func(callCtx context.Context, c client.WithWatch, object client.Object, opts ...client.DeleteOption) error {
					options := &client.DeleteOptions{}
					for _, opt := range opts {
						opt.ApplyToDelete(options)
					}
					require.NotNil(t, options.Preconditions)
					require.NotNil(t, options.Preconditions.UID)
					assert.Equal(t, object.GetUID(), *options.Preconditions.UID)
					if object.GetName() == pod.Name {
						if !tc.replace {
							return fmt.Errorf("API unavailable")
						}
						require.NoError(t, c.Delete(callCtx, object))
						replacement := pod.DeepCopy()
						replacement.ResourceVersion = ""
						replacement.UID = "replacement-pod-uid"
						replacement.Status = corev1.PodStatus{Phase: corev1.PodRunning}
						require.NoError(t, c.Create(callCtx, replacement))
						// The fake client does not enforce UID preconditions; emulate the API server's conflict.
						return apierrors.NewConflict(schema.GroupResource{Resource: "pods"}, object.GetName(), fmt.Errorf("UID precondition failed"))
					}
					return c.Delete(callCtx, object, opts...)
				},
			})
			cleanupUnexpectedAdmissionPods(ctx)
			retained := &corev1.Pod{}
			require.NoError(t, base.Get(context.Background(), client.ObjectKeyFromObject(pod), retained))
			if tc.replace {
				assert.Equal(t, "replacement-pod-uid", string(retained.UID))
			}
			assert.True(t, apierrors.IsNotFound(base.Get(context.Background(), client.ObjectKeyFromObject(other), &corev1.Pod{})))
		})
	}
}

func admissionCleanupFixtures() (*OrchestratorContext, *appsv1.Deployment, *corev1.Pod) {
	cluster := &v1.Cluster{Metadata: &v1.Metadata{Name: "cluster", Workspace: "workspace"}}
	namespace := util.ClusterNamespace(cluster)
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "endpoint", Namespace: namespace, UID: "deployment-uid"}, Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"endpoint": "endpoint"}}}}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "failed-pod", Namespace: namespace, UID: "pod-uid", Labels: map[string]string{"endpoint": "endpoint"}},
		Status:     corev1.PodStatus{Phase: corev1.PodFailed, Reason: "UnexpectedAdmissionError"},
	}
	return &OrchestratorContext{
		Cluster: cluster, Endpoint: &v1.Endpoint{Metadata: &v1.Metadata{Name: deployment.Name}}, logger: klog.Background(),
	}, deployment, pod
}

func admissionCleanupClient(t *testing.T, objects ...client.Object) client.WithWatch {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}
