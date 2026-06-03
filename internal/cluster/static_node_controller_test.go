package cluster

import (
	"context"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
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
	status v1.StaticNodeStatus
}

func (f *fakeStaticNodeStore) UpdateStaticNodeStatus(
	_ context.Context,
	_ *v1.StaticNode,
	status v1.StaticNodeStatus,
) error {
	f.status = status

	return nil
}

type fakeStaticNodeRunnerFactory struct {
	runner StaticNodeCommandRunner
}

func (f *fakeStaticNodeRunnerFactory) NewStaticNodeRunner(
	_ context.Context,
	_ *v1.StaticNode,
) (StaticNodeCommandRunner, error) {
	return f.runner, nil
}
