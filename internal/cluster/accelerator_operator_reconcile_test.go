package cluster

import (
	"context"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	accelerator "github.com/neutree-ai/neutree/internal/accelerator"
	acceleratormocks "github.com/neutree-ai/neutree/internal/accelerator/mocks"
	"github.com/neutree-ai/neutree/internal/accelerator/operator"
	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
	"github.com/stretchr/testify/mock"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type testClusterAcceleratorOperator struct {
	reconciled bool
	status     *v1.ComponentStatus
}

func (o *testClusterAcceleratorOperator) Name() string { return "test-operator" }

func (o *testClusterAcceleratorOperator) AcceleratorType() string { return "test-gpu" }

func (o *testClusterAcceleratorOperator) Reconcile(context.Context, *operator.OperatorContext) (operator.OperatorResult, error) {
	o.reconciled = true
	return operator.OperatorResult{}, nil
}

func (o *testClusterAcceleratorOperator) Status(context.Context, *operator.OperatorContext) (*v1.ComponentStatus, error) {
	return o.status, nil
}

func (o *testClusterAcceleratorOperator) Delete(context.Context, *operator.OperatorContext) error {
	return nil
}

func TestReconcileAcceleratorOperatorsDispatchesOperatorAndSkipsLegacyTypes(t *testing.T) {
	manager := &acceleratormocks.MockManager{}
	operatorImpl := &testClusterAcceleratorOperator{status: &v1.ComponentStatus{Phase: v1.ComponentPhaseReady, Managed: true}}
	acceleratorPlugin := &plugin.GPUAcceleratorPlugin{}

	manager.On("SupportPlugins").Return([]string{"test-gpu"}).Once()
	manager.On("GetPlugin", "test-gpu").Return(acceleratorPlugin, true).Once()
	manager.On("PluginDependencyDeployMode", "test-gpu", string(v1.KubernetesClusterType)).Return(accelerator.DependencyDeployModeOperator).Once()
	manager.On("OperatorForKubernetesCluster", "test-gpu", mock.Anything).Return(operatorImpl, nil).Once()

	reconciler := &NativeKubernetesClusterReconciler{acceleratorMgr: manager}
	cluster := &v1.Cluster{
		Metadata: &v1.Metadata{Name: "test-cluster", Workspace: "default"},
		Spec:     &v1.ClusterSpec{Type: v1.KubernetesClusterType},
	}
	ctx := &ReconcileContext{
		Cluster:          cluster,
		Ctx:              context.Background(),
		ctrClient:        fake.NewClientBuilder().WithScheme(testClusterScheme(t)).Build(),
		clusterNamespace: "neutree-system",
	}

	owned, err := reconciler.reconcileAcceleratorOperators(ctx, "registry.example.com/neutree/")
	if err != nil {
		t.Fatalf("reconcileAcceleratorOperators() error = %v", err)
	}
	if _, ok := owned["test-gpu"]; !ok || !operatorImpl.reconciled {
		t.Fatalf("operator was not reconciled: owned=%v reconciled=%v", owned, operatorImpl.reconciled)
	}
	if cluster.Status == nil || cluster.Status.ComponentStatus["accelerator-operator/test-gpu"] != operatorImpl.status {
		t.Fatalf("operator status was not recorded: %#v", cluster.Status)
	}
	manager.AssertExpectations(t)
}

func testClusterScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}
