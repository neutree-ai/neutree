package controllers

import (
	"context"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	clusterreconcile "github.com/neutree-ai/neutree/internal/cluster"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStaticNodeControllerReconcile(t *testing.T) {
	store := &fakeControllerStaticNodeStore{}
	runner := &fakeControllerStaticNodeRunner{
		responses: []fakeControllerStaticNodeRunnerResponse{
			{
				command: "docker image inspect --format='{{index .RepoDigests 0}}' 'registry.example.com/neutree/neutree-serve:v1.2.0'",
				output:  "registry.example.com/neutree/neutree-serve@sha256:ready\n",
			},
		},
	}
	controller, err := NewStaticNodeController(&StaticNodeControllerOption{
		Store:         store,
		RunnerFactory: &fakeControllerStaticNodeRunnerFactory{runner: runner},
	})
	require.NoError(t, err)

	err = controller.Reconcile(controllerStaticNode())

	require.NoError(t, err)
	assert.Equal(t, v1.StaticNodePhaseReady, store.status.Phase)
	require.NotNil(t, store.status.Warm)
	assert.True(t, store.status.Warm.Ready)
	assert.Equal(t, 1, runner.calls)
}

func TestStaticNodeControllerReconcileRejectsWrongType(t *testing.T) {
	controller, err := NewStaticNodeController(&StaticNodeControllerOption{
		Store: &fakeControllerStaticNodeStore{},
	})
	require.NoError(t, err)

	err = controller.Reconcile(&v1.StaticNodeCluster{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to assert obj to *v1.StaticNode")
}

type fakeControllerStaticNodeStore struct {
	status v1.StaticNodeStatus
}

var _ clusterreconcile.StaticNodeStore = (*fakeControllerStaticNodeStore)(nil)

func (f *fakeControllerStaticNodeStore) UpdateStaticNodeStatus(
	_ context.Context,
	_ *v1.StaticNode,
	status v1.StaticNodeStatus,
) error {
	f.status = status

	return nil
}

type fakeControllerStaticNodeRunnerFactory struct {
	runner clusterreconcile.StaticNodeCommandRunner
}

var _ clusterreconcile.StaticNodeRunnerFactory = (*fakeControllerStaticNodeRunnerFactory)(nil)

func (f *fakeControllerStaticNodeRunnerFactory) NewStaticNodeRunner(
	_ context.Context,
	_ *v1.StaticNode,
) (clusterreconcile.StaticNodeCommandRunner, error) {
	return f.runner, nil
}

type fakeControllerStaticNodeRunner struct {
	responses []fakeControllerStaticNodeRunnerResponse
	calls     int
}

var _ clusterreconcile.StaticNodeCommandRunner = (*fakeControllerStaticNodeRunner)(nil)

type fakeControllerStaticNodeRunnerResponse struct {
	command string
	output  string
	err     error
}

func (f *fakeControllerStaticNodeRunner) Run(_ context.Context, command string) (string, error) {
	if f.calls >= len(f.responses) {
		return "", assert.AnError
	}

	response := f.responses[f.calls]
	f.calls++
	if response.command != "" && response.command != command {
		return "", assert.AnError
	}

	return response.output, response.err
}

func controllerStaticNode() *v1.StaticNode {
	return &v1.StaticNode{
		Metadata: &v1.Metadata{
			Workspace: "default",
			Name:      "head-0",
		},
		Spec: &v1.StaticNodeSpec{
			Cluster: "static-a",
			IP:      "10.0.0.10",
			Role:    v1.StaticNodeRoleHead,
			Warm: &v1.WarmSpec{
				Images: []v1.WarmImageSpec{
					{
						Name:     "ray-runtime",
						Ref:      "registry.example.com/neutree/neutree-serve:v1.2.0",
						Required: true,
					},
				},
			},
		},
	}
}
