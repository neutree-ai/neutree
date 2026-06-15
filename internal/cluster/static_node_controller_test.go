package cluster

import (
	"context"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStaticNodeControllerReconcileUpdatesWarmStatus(t *testing.T) {
	node := staticNodeWithWarmImages([]v1.WarmImageSpec{
		{Name: "ray-runtime", Ref: "registry.example.com/neutree/neutree-serve:v1.2.0", Required: true},
	})
	store := &fakeStaticNodeStore{}
	runner := &fakeStaticNodeRunner{
		responses: []fakeStaticNodeResponse{
			{
				command: "docker image inspect --format='{{index .RepoDigests 0}}' 'registry.example.com/neutree/neutree-serve:v1.2.0'",
				output:  "registry.example.com/neutree/neutree-serve@sha256:ready\n",
			},
		},
	}

	err := (&StaticNodeController{
		Store:         store,
		RunnerFactory: &fakeStaticNodeRunnerFactory{runner: runner},
	}).Reconcile(context.Background(), node)

	require.NoError(t, err)
	assert.Equal(t, v1.StaticNodePhaseReady, store.status.Phase)
	require.NotNil(t, store.status.Warm)
	assert.True(t, store.status.Warm.Ready)
	assert.Equal(t, "registry.example.com/neutree/neutree-serve@sha256:ready", store.status.Warm.Images[0].Digest)
	assert.Equal(t, 1, runner.calls)
}

func TestStaticNodeControllerReconcileRunsDiscoveryForEmptyDesiredState(t *testing.T) {
	node := &v1.StaticNode{
		Spec: &v1.StaticNodeSpec{
			Cluster: "static-a",
			IP:      "10.0.0.10",
			Role:    v1.StaticNodeRoleHead,
		},
	}
	store := &fakeStaticNodeStore{}
	runnerFactory := &fakeStaticNodeRunnerFactory{runner: &fakeStaticNodeRunner{}}
	detector := &fakeStaticNodeAcceleratorManager{
		accelerator: v1.CPUStaticNodeAcceleratorStatus(),
	}

	err := (&StaticNodeController{
		Store:         store,
		RunnerFactory: runnerFactory,
		Reconciler:    &StaticNodeReconciler{AcceleratorManager: detector},
	}).Reconcile(context.Background(), node)

	require.NoError(t, err)
	assert.Equal(t, 1, runnerFactory.calls)
	assert.Equal(t, v1.StaticNodePhaseReady, store.status.Phase)
	require.NotNil(t, store.status.Accelerator)
	assert.Equal(t, v1.StaticNodeAcceleratorTypeCPU, store.status.Accelerator.Type)
	assert.Empty(t, store.status.ErrorMessage)
}

func TestStaticNodeControllerReconcileRequiredWarmFailure(t *testing.T) {
	node := staticNodeWithWarmImages([]v1.WarmImageSpec{
		{Name: "ray-runtime", Ref: "registry.example.com/neutree/neutree-serve:v1.2.0", Required: true},
	})
	store := &fakeStaticNodeStore{}
	runner := &fakeStaticNodeRunner{
		responses: []fakeStaticNodeResponse{
			{
				command: "docker image inspect --format='{{index .RepoDigests 0}}' 'registry.example.com/neutree/neutree-serve:v1.2.0'",
				err:     assert.AnError,
			},
			{
				command: "docker pull 'registry.example.com/neutree/neutree-serve:v1.2.0'",
				err:     assert.AnError,
			},
		},
	}

	err := (&StaticNodeController{
		Store:         store,
		RunnerFactory: &fakeStaticNodeRunnerFactory{runner: runner},
	}).Reconcile(context.Background(), node)

	require.Error(t, err)
	assert.Equal(t, v1.StaticNodePhaseFailed, store.status.Phase)
	assert.Contains(t, store.status.ErrorMessage, "assert.AnError")
	require.NotNil(t, store.status.Warm)
	assert.False(t, store.status.Warm.Ready)
}

type fakeStaticNodeStore struct {
	status       v1.StaticNodeStatus
	deletedNodes []*v1.StaticNode
}

func (f *fakeStaticNodeStore) UpdateStaticNodeStatus(
	_ context.Context,
	_ *v1.StaticNode,
	status v1.StaticNodeStatus,
) error {
	f.status = status

	return nil
}

func (f *fakeStaticNodeStore) HardDeleteStaticNode(_ context.Context, node *v1.StaticNode) error {
	f.deletedNodes = append(f.deletedNodes, node)

	return nil
}

type fakeStaticNodeRunnerFactory struct {
	runner StaticNodeCommandRunner
	calls  int
}

func (f *fakeStaticNodeRunnerFactory) NewStaticNodeRunner(
	_ context.Context,
	_ *v1.StaticNode,
) (StaticNodeCommandRunner, error) {
	f.calls++

	return f.runner, nil
}

type fakeStaticNodeAcceleratorManager struct {
	accelerator v1.StaticNodeAcceleratorStatus
	err         error
}

func (f *fakeStaticNodeAcceleratorManager) DetectAccelerator(
	_ context.Context,
	_ accelerator.NodeCommandRunner,
) (*v1.StaticNodeAcceleratorStatus, error) {
	if f.err != nil {
		return nil, f.err
	}

	return &f.accelerator, nil
}
