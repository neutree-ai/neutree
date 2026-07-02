package controllers

import (
	"context"
	"errors"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	clusterreconcile "github.com/neutree-ai/neutree/internal/cluster"
	commandrunner "github.com/neutree-ai/neutree/pkg/command_runner"
	"github.com/neutree-ai/neutree/pkg/storage"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStaticNodeControllerReconcile(t *testing.T) {
	objectStorage := &fakeControllerStaticNodeObjectStorage{}
	runner := &fakeControllerStaticNodeRunner{
		responses: []fakeControllerStaticNodeRunnerResponse{
			{
				command: "docker image inspect --format='{{index .RepoDigests 0}}' 'registry.example.com/neutree/neutree-serve:v1.2.0'",
				output:  "registry.example.com/neutree/neutree-serve@sha256:ready\n",
			},
		},
	}
	controller, err := NewStaticNodeController(&StaticNodeControllerOption{
		Storage: newTestStaticNodeStorage(objectStorage),
		RunnerFactory: &fakeControllerStaticNodeRunnerFactory{
			runner: runner,
		},
	})
	require.NoError(t, err)

	err = controller.Reconcile(controllerStaticNode())

	require.NoError(t, err)
	statusObj := objectStorage.updatedStatus["8"]
	require.NotNil(t, statusObj)
	require.NotNil(t, statusObj.Status)
	assert.Equal(t, v1.StaticNodePhaseReconciling, statusObj.Status.Phase)
	require.NotNil(t, statusObj.Status.Warm)
	assert.True(t, statusObj.Status.Warm.Ready)
	require.Len(t, objectStorage.updatedStatusHistory, 3)
	assert.NotNil(t, objectStorage.updatedStatusHistory[0].Status.Accelerator)
	assert.Nil(t, objectStorage.updatedStatusHistory[0].Status.Warm)
	require.NotNil(t, objectStorage.updatedStatusHistory[1].Status.Warm)
	assert.True(t, objectStorage.updatedStatusHistory[1].Status.Warm.Ready)
	assert.Equal(t, v1.StaticNodePhaseReconciling, objectStorage.updatedStatusHistory[2].Status.Phase)
	assert.Equal(t, 1, runner.calls)
}

func TestStaticNodeControllerReconcileRejectsWrongType(t *testing.T) {
	controller, err := NewStaticNodeController(&StaticNodeControllerOption{
		Storage: newTestStaticNodeStorage(&fakeControllerStaticNodeObjectStorage{}),
	})
	require.NoError(t, err)

	err = controller.Reconcile(&v1.StaticNodeCluster{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to assert obj to *v1.StaticNode")
}

func TestNewStaticNodeControllerRequiresStorage(t *testing.T) {
	controller, err := NewStaticNodeController(&StaticNodeControllerOption{})

	require.Error(t, err)
	assert.Nil(t, controller)
	assert.Contains(t, err.Error(), "storage is required")
}

func TestStaticNodeControllerForceDeleteHardDeletesAfterBestEffortCleanup(t *testing.T) {
	objectStorage := &fakeControllerStaticNodeObjectStorage{}
	node := controllerStaticNode()
	node.Metadata.DeletionTimestamp = "2026-06-15T16:47:17Z"
	node.Metadata.Annotations = map[string]string{
		"neutree.ai/force-delete": "true",
	}
	controller, err := NewStaticNodeController(&StaticNodeControllerOption{
		Storage: newTestStaticNodeStorage(objectStorage),
		RunnerFactory: &fakeControllerStaticNodeRunnerFactory{
			runner: &fakeControllerStaticNodeRunner{
				responses: []fakeControllerStaticNodeRunnerResponse{
					{err: errors.New("remote cleanup failed")},
				},
			},
		},
	})
	require.NoError(t, err)

	err = controller.Reconcile(node)

	require.NoError(t, err)
	assert.Equal(t, []string{"8"}, objectStorage.deletedIDs)
	assert.Empty(t, objectStorage.updatedStatus)
}

func TestStaticNodeControllerDeleteFailureUpdatesStatusWithoutHardDelete(t *testing.T) {
	objectStorage := &fakeControllerStaticNodeObjectStorage{}
	node := controllerStaticNode()
	node.Metadata.DeletionTimestamp = "2026-06-15T16:47:17Z"
	controller, err := NewStaticNodeController(&StaticNodeControllerOption{
		Storage: newTestStaticNodeStorage(objectStorage),
		RunnerFactory: &fakeControllerStaticNodeRunnerFactory{
			runner: &fakeControllerStaticNodeRunner{
				responses: []fakeControllerStaticNodeRunnerResponse{
					{err: errors.New("remote cleanup failed")},
				},
			},
		},
	})
	require.NoError(t, err)

	err = controller.Reconcile(node)

	require.Error(t, err)
	assert.Empty(t, objectStorage.deletedIDs)
	statusObj := objectStorage.updatedStatus["8"]
	require.NotNil(t, statusObj)
	require.NotNil(t, statusObj.Status)
	assert.Equal(t, v1.StaticNodePhaseFailed, statusObj.Status.Phase)
	assert.Contains(t, statusObj.Status.ErrorMessage, "remote cleanup failed")
}

func TestStaticNodeControllerReconcileAlwaysCreatesRunner(t *testing.T) {
	objectStorage := &fakeControllerStaticNodeObjectStorage{}
	node := controllerStaticNode()
	node.Spec.Warm = nil
	node.Spec.Components = nil
	runnerCreated := false
	controller, err := NewStaticNodeController(&StaticNodeControllerOption{
		Storage: newTestStaticNodeStorage(objectStorage),
		RunnerFactory: &fakeControllerStaticNodeRunnerFactory{
			buildRunner: func(context.Context, *v1.StaticNode) (clusterreconcile.StaticNodeCommandRunner, error) {
				runnerCreated = true

				return &fakeControllerStaticNodeRunner{}, nil
			},
		},
	})
	require.NoError(t, err)

	err = controller.Reconcile(node)

	require.NoError(t, err)
	assert.True(t, runnerCreated)
	statusObj := objectStorage.updatedStatus["8"]
	require.NotNil(t, statusObj)
	require.NotNil(t, statusObj.Status)
	assert.Equal(t, v1.StaticNodePhaseReconciling, statusObj.Status.Phase)
}

func newTestStaticNodeStorage(objectStorage *fakeControllerStaticNodeObjectStorage) storage.Storage {
	return &fakeControllerStaticNodeStorage{
		MockStorage:   &storagemocks.MockStorage{},
		objectStorage: objectStorage,
	}
}

type fakeControllerStaticNodeStorage struct {
	*storagemocks.MockStorage
	objectStorage *fakeControllerStaticNodeObjectStorage
}

func (f *fakeControllerStaticNodeStorage) ListStaticNode(storage.ListOption) ([]v1.StaticNode, error) {
	return nil, nil
}

func (f *fakeControllerStaticNodeStorage) CreateStaticNode(data *v1.StaticNode) error {
	return nil
}

func (f *fakeControllerStaticNodeStorage) UpdateStaticNode(id string, data *v1.StaticNode) error {
	if f.objectStorage.updatedStatus == nil {
		f.objectStorage.updatedStatus = map[string]*v1.StaticNode{}
	}

	f.objectStorage.updatedStatus[id] = data
	f.objectStorage.updatedStatusHistory = append(f.objectStorage.updatedStatusHistory, data)

	return nil
}

func (f *fakeControllerStaticNodeStorage) DeleteStaticNode(id string) error {
	f.objectStorage.deletedIDs = append(f.objectStorage.deletedIDs, id)

	return nil
}

type fakeControllerStaticNodeObjectStorage struct {
	updatedStatus        map[string]*v1.StaticNode
	updatedStatusHistory []*v1.StaticNode
	deletedIDs           []string
}

type fakeControllerStaticNodeRunnerFactory struct {
	runner      clusterreconcile.StaticNodeCommandRunner
	buildRunner func(context.Context, *v1.StaticNode) (clusterreconcile.StaticNodeCommandRunner, error)
}

func (f *fakeControllerStaticNodeRunnerFactory) NewStaticNodeRunner(
	ctx context.Context,
	node *v1.StaticNode,
) (clusterreconcile.StaticNodeCommandRunner, error) {
	if f.buildRunner != nil {
		return f.buildRunner(ctx, node)
	}

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

func (f *fakeControllerStaticNodeRunner) Close() error {
	return nil
}

func (f *fakeControllerStaticNodeRunner) Files() commandrunner.FileClient {
	return nil
}

func controllerStaticNode() *v1.StaticNode {
	return &v1.StaticNode{
		ID: 8,
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
