package cluster

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator"
	publicaccelerator "github.com/neutree-ai/neutree/pkg/accelerator"
)

func TestKubernetesAcceleratorOperatorUsesRegisteredFactory(t *testing.T) {
	for _, tc := range []struct {
		name, acceleratorType            string
		managed, wantOperator, wantError bool
	}{
		{name: "GPU factory runs without managed dependencies", acceleratorType: publicaccelerator.NvidiaAcceleratorType, wantOperator: true},
		{name: "managed driver failure reported", acceleratorType: publicaccelerator.NvidiaAcceleratorType, managed: true, wantOperator: true, wantError: true},
		{name: "unregistered factory remains Legacy", acceleratorType: v1.AcceleratorTypeAMDGPU.String()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, corev1.AddToScheme(scheme))
			require.NoError(t, appsv1.AddToScheme(scheme))
			scheme.AddKnownTypeWithName(schema.GroupVersionKind{Group: "nvidia.com", Version: "v1", Kind: "ClusterPolicy"}, &unstructured.Unstructured{})
			scheme.AddKnownTypeWithName(schema.GroupVersionKind{Group: "nvidia.com", Version: "v1", Kind: "ClusterPolicyList"}, &unstructured.UnstructuredList{})
			objects := []client.Object{&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "gpu-1", Labels: map[string]string{publicaccelerator.NvidiaGPUDiscoveryLabelKey: "true"}}}}
			if tc.managed {
				objects = append(objects, &unstructured.Unstructured{Object: map[string]interface{}{
					"apiVersion": "nvidia.com/v1", "kind": "ClusterPolicy", "metadata": map[string]interface{}{"name": "policy"},
					"spec": map[string]interface{}{"driver": map[string]interface{}{"enabled": true}, "toolkit": map[string]interface{}{"enabled": true}},
				}})
			}
			ctrlClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
			reconcileCtx := &ReconcileContext{Ctx: context.Background(), ctrClient: ctrlClient, Cluster: &v1.Cluster{
				Metadata: &v1.Metadata{Name: "cluster", Workspace: "workspace"},
				Spec:     &v1.ClusterSpec{Type: v1.KubernetesClusterType, Config: &v1.ClusterConfig{AcceleratorType: ptr.To(tc.acceleratorType)}},
			}}
			reconciler := &NativeKubernetesClusterReconciler{acceleratorMgr: accelerator.NewManager()}
			err := reconciler.reconcileAcceleratorOperators(reconcileCtx)
			if tc.wantError {
				require.ErrorContains(t, err, "gpu-1 has no driver Pod")
			} else {
				require.NoError(t, err)
			}
			if tc.wantOperator {
				require.Len(t, reconcileCtx.acceleratorOperators, 1)
				status := reconcileCtx.Cluster.Status.ComponentStatus["accelerator-operator/nvidia"]
				require.NotNil(t, status)
				if tc.wantError {
					require.Equal(t, v1.ComponentPhaseNotReady, status.Phase)
				} else {
					require.Equal(t, v1.ComponentPhaseReady, status.Phase)
				}
			} else {
				require.Empty(t, reconcileCtx.acceleratorOperators)
			}
		})
	}
}
