package cluster

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/neutree-ai/neutree/api/v1"
	accelerator "github.com/neutree-ai/neutree/internal/accelerator"
	acceleratormocks "github.com/neutree-ai/neutree/internal/accelerator/mocks"
	"github.com/neutree-ai/neutree/internal/accelerator/operator"
	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
	"github.com/neutree-ai/neutree/pkg/metricsconfig"
)

type testClusterAcceleratorOperator struct {
	reconciled bool
	deleted    bool
	status     *v1.ComponentStatus
}

func (o *testClusterAcceleratorOperator) Name() string { return "test-operator" }

func (o *testClusterAcceleratorOperator) AcceleratorType() string {
	return operator.NVIDIAAcceleratorType
}

func (o *testClusterAcceleratorOperator) Reconcile(context.Context, *operator.OperatorContext) (operator.OperatorResult, error) {
	o.reconciled = true
	return operator.OperatorResult{}, nil
}

func (o *testClusterAcceleratorOperator) Status(context.Context, *operator.OperatorContext) (*v1.ComponentStatus, error) {
	return o.status, nil
}

func (o *testClusterAcceleratorOperator) Delete(context.Context, *operator.OperatorContext) error {
	o.deleted = true
	return nil
}

func testClusterScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

func TestOperatorNotReadyPreservesOwnershipAndBlocksCluster(t *testing.T) {
	manager := &acceleratormocks.MockManager{}
	operatorImpl := &testClusterAcceleratorOperator{status: &v1.ComponentStatus{Phase: v1.ComponentPhaseNotReady, Reason: "BaseComponentsNotReady", Message: "dcgm-exporter is not ready"}}
	manager.On("SupportPlugins").Return([]string{operator.NVIDIAAcceleratorType})
	manager.On("GetPlugin", operator.NVIDIAAcceleratorType).Return(&plugin.GPUAcceleratorPlugin{}, true)
	manager.On("PluginDependencyDeployMode", operator.NVIDIAAcceleratorType, string(v1.KubernetesClusterType)).Return(accelerator.DependencyDeployModeOperator)
	manager.On("OperatorForKubernetesCluster", operator.NVIDIAAcceleratorType, mock.Anything).Return(operatorImpl, nil)
	reconciler := &NativeKubernetesClusterReconciler{acceleratorMgr: manager}
	cluster := &v1.Cluster{Metadata: &v1.Metadata{Name: "cluster-a", Workspace: "default"}, Spec: &v1.ClusterSpec{Type: v1.KubernetesClusterType}}
	ctx := &ReconcileContext{Cluster: cluster, Ctx: context.Background(), ctrClient: fake.NewClientBuilder().WithScheme(testClusterScheme(t)).Build(), clusterNamespace: "tenant-a", metricsTargets: metricsconfig.NewRegistry()}
	owned, err := reconciler.reconcileAcceleratorOperators(ctx, "")
	if err == nil || !strings.Contains(err.Error(), "dcgm-exporter") {
		t.Fatalf("NotReady did not block cluster: %v", err)
	}
	if _, found := owned[operator.NVIDIAAcceleratorType]; !found {
		t.Fatal("NotReady accelerator lost Operator ownership")
	}
	if ctx.metricsTargets == nil {
		t.Fatal("NotReady dropped operator metric registry")
	}
}

func TestNamespaceAlreadyDeletedStillCleansAcceleratorOperator(t *testing.T) {
	manager := &acceleratormocks.MockManager{}
	operatorImpl := &testClusterAcceleratorOperator{}
	manager.On("SupportPlugins").Return([]string{operator.NVIDIAAcceleratorType})
	manager.On("GetPlugin", operator.NVIDIAAcceleratorType).Return(&plugin.GPUAcceleratorPlugin{}, true)
	manager.On("PluginDependencyDeployMode", operator.NVIDIAAcceleratorType, string(v1.KubernetesClusterType)).Return(accelerator.DependencyDeployModeOperator)
	manager.On("OperatorForKubernetesCluster", operator.NVIDIAAcceleratorType, mock.Anything).Return(operatorImpl, nil)
	reconciler := &NativeKubernetesClusterReconciler{acceleratorMgr: manager}
	cluster := &v1.Cluster{Metadata: &v1.Metadata{Name: "cluster-a", Workspace: "default"}, Spec: &v1.ClusterSpec{Type: v1.KubernetesClusterType}}
	ctx := &ReconcileContext{Cluster: cluster, Ctx: context.Background(), ctrClient: fake.NewClientBuilder().WithScheme(testClusterScheme(t)).Build(), clusterNamespace: "tenant-a", kubernetesClusterConfig: &v1.KubernetesClusterConfig{}}
	if err := reconciler.reconcileDelete(ctx); err != nil {
		t.Fatalf("delete after namespace removal: %v", err)
	}
	if !operatorImpl.deleted {
		t.Fatal("namespace disappearance skipped Operator cluster-scoped cleanup")
	}
}

func TestOperatorNvidiaWithUnsupportedLegacyAMDRetainsHAMiControlPlane(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			manager := &acceleratormocks.MockManager{}
			operatorImpl := &testClusterAcceleratorOperator{}
			manager.On("SupportPlugins").Return([]string{"nvidia_gpu", "amd_gpu"})
			manager.On("GetPlugin", "amd_gpu").Return(&plugin.AMDGPUAcceleratorPlugin{}, true)
			reconciler := &NativeKubernetesClusterReconciler{acceleratorMgr: manager}
			cluster := &v1.Cluster{Metadata: &v1.Metadata{Name: "cluster-a", Workspace: "default"}, Spec: &v1.ClusterSpec{Type: v1.KubernetesClusterType, Version: "v1.2.1", AcceleratorVirtualization: &v1.AcceleratorVirtualizationSpec{Enabled: enabled}}}
			ctx := &ReconcileContext{Cluster: cluster, Ctx: context.Background(), kubernetesClusterConfig: &v1.KubernetesClusterConfig{}}
			ctx.acceleratorOperators = map[string]operator.AcceleratorOperator{"nvidia_gpu": operatorImpl}
			reconcile, remove := reconciler.ComputeAdditionalComponents(ctx, "")
			if enabled {
				if len(reconcile) != 2 || len(remove) != 0 {
					t.Fatalf("Operator mode lost HAMi control plane: reconcile=%d remove=%d", len(reconcile), len(remove))
				}
			} else if len(reconcile) != 1 || len(remove) != 1 {
				t.Fatalf("disabled virtualization did not remove HAMi control plane: reconcile=%d remove=%d", len(reconcile), len(remove))
			}
		})
	}
}
