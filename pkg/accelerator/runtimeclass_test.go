package accelerator

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	nodev1 "k8s.io/api/node/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestSelectRuntimeClass(t *testing.T) {
	cluster := runtimeClassCluster()
	owned := ownedRuntimeClass()
	for _, tc := range []struct {
		name       string
		configured interface{}
		classes    []client.Object
		want       string
		wantError  string
	}{
		{name: "configured class has priority and may use a custom handler", configured: "custom", classes: []client.Object{
			runtimeClass("custom", "custom-handler"), runtimeClass("nvidia", "nvidia"), owned,
		}, want: "custom"},
		{name: "configured class not created uses standard class", configured: "pending", classes: []client.Object{runtimeClass("nvidia", "nvidia")}, want: "nvidia"},
		{name: "missing configuration uses standard class", classes: []client.Object{runtimeClass("nvidia", "nvidia")}, want: "nvidia"},
		{name: "empty configuration uses standard class", configured: "", classes: []client.Object{runtimeClass("nvidia", "nvidia")}, want: "nvidia"},
		{name: "standard class takes priority over owned fallback", classes: []client.Object{runtimeClass("nvidia", "nvidia"), owned}, want: "nvidia"},
		{name: "arbitrary class with nvidia handler is ignored", classes: []client.Object{runtimeClass("other", "nvidia")}},
		{name: "standard class with wrong handler is ignored", classes: []client.Object{runtimeClass("nvidia", "other")}},
		{name: "owned fallback is reused", classes: []client.Object{owned}, want: NeutreeNvidiaRuntimeClass},
		{name: "wrong standard handler falls back to owned class", classes: []client.Object{runtimeClass("nvidia", "other"), owned}, want: NeutreeNvidiaRuntimeClass},
		{name: "foreign fallback conflicts", classes: []client.Object{runtimeClass(NeutreeNvidiaRuntimeClass, "nvidia")}, wantError: "not managed by"},
		{name: "owned fallback with wrong handler conflicts", classes: []client.Object{func() *nodev1.RuntimeClass {
			value := ownedRuntimeClass()
			value.Handler = "other"
			return value
		}()}, wantError: "handler"},
		{name: "malformed policy configuration is an error", configured: true, wantError: "operator.runtimeClass"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctrlClient := runtimeClassClient(t, tc.classes...)
			var policies []GPUOperatorClusterPolicy
			if tc.configured != nil {
				policies = []GPUOperatorClusterPolicy{{Name: "policy", Spec: map[string]interface{}{"operator": map[string]interface{}{"runtimeClass": tc.configured}}}}
			}
			before := &nodev1.RuntimeClassList{}
			require.NoError(t, ctrlClient.List(context.Background(), before))
			selected, err := SelectRuntimeClass(context.Background(), ctrlClient, cluster, policies)
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.want, selected)
			}
			after := &nodev1.RuntimeClassList{}
			require.NoError(t, ctrlClient.List(context.Background(), after))
			require.Equal(t, before, after, "selection must not modify resources")
		})
	}
}

func TestRuntimeClassAPIErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(client.Client) error
	}{
		{name: "selection propagates permission errors", run: func(ctrlClient client.Client) error {
			_, err := SelectRuntimeClass(context.Background(), ctrlClient, runtimeClassCluster(), nil)
			return err
		}},
		{name: "delete propagates query errors", run: func(ctrlClient client.Client) error {
			return DeleteRuntimeClass(context.Background(), ctrlClient, runtimeClassCluster())
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			permissionError := apierrors.NewForbidden(schema.GroupResource{Group: "node.k8s.io", Resource: "runtimeclasses"}, "nvidia", errors.New("denied"))
			ctrlClient := interceptor.NewClient(runtimeClassClient(t), interceptor.Funcs{
				Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
					return permissionError
				},
			})
			require.ErrorIs(t, tc.run(ctrlClient), permissionError)
		})
	}
}

func TestEnsureRuntimeClass(t *testing.T) {
	standard := runtimeClass("nvidia", "nvidia")
	standard.Scheduling = &nodev1.Scheduling{NodeSelector: map[string]string{"gpu-pool": "selected"}}

	for _, tc := range []struct {
		name      string
		classes   []client.Object
		createErr bool
		raceOwner string
		wantError string
		wantClass string
	}{
		{name: "creates owned fallback", wantClass: NeutreeNvidiaRuntimeClass},
		{name: "reuses standard class without modifying scheduling", classes: []client.Object{standard}, wantClass: "nvidia"},
		{name: "reuses own fallback", classes: []client.Object{ownedRuntimeClass()}, wantClass: NeutreeNvidiaRuntimeClass},
		{name: "does not adopt fallback", classes: []client.Object{runtimeClass(NeutreeNvidiaRuntimeClass, "nvidia")}, wantError: "not managed by"},
		{name: "create failure propagates", createErr: true, wantError: "denied"},
		{name: "concurrent own creation is idempotent", raceOwner: "own", wantClass: NeutreeNvidiaRuntimeClass},
		{name: "concurrent foreign creation conflicts", raceOwner: "foreign", wantError: "not managed by"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctrlClient := interceptor.NewClient(runtimeClassClient(t, tc.classes...), interceptor.Funcs{
				Get: func(ctx context.Context, inner client.WithWatch, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
					if tc.raceOwner == "" {
						return inner.Get(ctx, key, object, opts...)
					}

					actual := &nodev1.RuntimeClass{}
					if err := inner.Get(ctx, key, actual, opts...); err != nil {
						return err
					}

					// REST decoding preserves destination fields omitted from the response.
					data, err := json.Marshal(actual)
					require.NoError(t, err)
					return json.Unmarshal(data, object)
				},
				Create: func(ctx context.Context, inner client.WithWatch, object client.Object, opts ...client.CreateOption) error {
					if tc.createErr {
						return apierrors.NewForbidden(schema.GroupResource{Group: "node.k8s.io", Resource: "runtimeclasses"}, object.GetName(), errors.New("denied"))
					}
					if tc.raceOwner != "" {
						created := ownedRuntimeClass()
						if tc.raceOwner == "foreign" {
							created.Labels = nil
						}
						require.NoError(t, inner.Create(ctx, created))
						return apierrors.NewAlreadyExists(schema.GroupResource{Group: "node.k8s.io", Resource: "runtimeclasses"}, created.Name)
					}
					return inner.Create(ctx, object, opts...)
				},
			})
			before := &nodev1.RuntimeClassList{}
			require.NoError(t, ctrlClient.List(context.Background(), before))

			err := EnsureRuntimeClass(context.Background(), ctrlClient, runtimeClassCluster(), nil)
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
				return
			}
			require.NoError(t, err)
			require.NoError(t, EnsureRuntimeClass(context.Background(), ctrlClient, runtimeClassCluster(), nil))
			selected, err := SelectRuntimeClass(context.Background(), ctrlClient, runtimeClassCluster(), nil)
			require.NoError(t, err)
			require.Equal(t, tc.wantClass, selected)
			classes := &nodev1.RuntimeClassList{}
			require.NoError(t, ctrlClient.List(context.Background(), classes))
			require.Len(t, classes.Items, 1)
			if len(tc.classes) > 0 {
				require.Equal(t, before, classes, "reused classes must remain unchanged")
			}

			if selected == NeutreeNvidiaRuntimeClass {
				require.Equal(t, ownedRuntimeClass().Labels, classes.Items[0].Labels)
			}
		})
	}
}

func TestDeleteRuntimeClass(t *testing.T) {
	for _, tc := range []struct {
		name   string
		labels map[string]string
		keep   bool
	}{
		{name: "deletes own fallback even after standard class appears", labels: ownedRuntimeClass().Labels},
		{name: "does not delete foreign fallback", keep: true},
		{name: "does not delete fallback owned by another cluster", labels: map[string]string{
			v1.NeutreeClusterLabelKey: "other", v1.NeutreeClusterWorkspaceLabelKey: "workspace", v1.LabelManagedBy: v1.LabelManagedByValue,
		}, keep: true},
		{name: "does not delete fallback owned by another workspace", labels: map[string]string{
			v1.NeutreeClusterLabelKey: "cluster", v1.NeutreeClusterWorkspaceLabelKey: "other", v1.LabelManagedBy: v1.LabelManagedByValue,
		}, keep: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fallback := runtimeClass(NeutreeNvidiaRuntimeClass, "nvidia")
			fallback.Labels = tc.labels
			ctrlClient := runtimeClassClient(t, fallback, runtimeClass("nvidia", "nvidia"))
			require.NoError(t, DeleteRuntimeClass(context.Background(), ctrlClient, runtimeClassCluster()))
			require.NoError(t, DeleteRuntimeClass(context.Background(), ctrlClient, runtimeClassCluster()))
			err := ctrlClient.Get(context.Background(), client.ObjectKey{Name: NeutreeNvidiaRuntimeClass}, &nodev1.RuntimeClass{})
			if tc.keep {
				require.NoError(t, err)
			} else {
				require.True(t, apierrors.IsNotFound(err))
			}
			require.NoError(t, ctrlClient.Get(context.Background(), client.ObjectKey{Name: "nvidia"}, &nodev1.RuntimeClass{}))
		})
	}
}

func runtimeClass(name, handler string) *nodev1.RuntimeClass {
	return &nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: name}, Handler: handler}
}

func ownedRuntimeClass() *nodev1.RuntimeClass {
	class := runtimeClass(NeutreeNvidiaRuntimeClass, "nvidia")
	class.Labels = map[string]string{
		v1.NeutreeClusterLabelKey: "cluster", v1.NeutreeClusterWorkspaceLabelKey: "workspace", v1.LabelManagedBy: v1.LabelManagedByValue,
	}
	return class
}

func runtimeClassCluster() *v1.Cluster {
	return &v1.Cluster{Metadata: &v1.Metadata{Name: "cluster", Workspace: "workspace"}}
}

func runtimeClassClient(t *testing.T, objects ...client.Object) client.WithWatch {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, nodev1.AddToScheme(scheme))
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}
