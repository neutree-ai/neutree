package controllers

import (
	"context"
	"errors"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	clusterreconcile "github.com/neutree-ai/neutree/internal/cluster"
	"github.com/neutree-ai/neutree/pkg/scheme"
	"github.com/neutree-ai/neutree/pkg/storage"
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
		Store: storage.NewStaticNodeObjectStore(objectStorage),
	})
	require.NoError(t, err)
	controller.newRunner = func(context.Context, *v1.StaticNode) (clusterreconcile.StaticNodeCommandRunner, error) {
		return runner, nil
	}

	err = controller.Reconcile(controllerStaticNode())

	require.NoError(t, err)
	statusObj, ok := objectStorage.updatedStatus["8"].(*v1.StaticNode)
	require.True(t, ok)
	require.NotNil(t, statusObj.Status)
	assert.Equal(t, v1.StaticNodePhaseReconciling, statusObj.Status.Phase)
	require.NotNil(t, statusObj.Status.Warm)
	assert.True(t, statusObj.Status.Warm.Ready)
	assert.Equal(t, 1, runner.calls)
}

func TestStaticNodeControllerReconcileRejectsWrongType(t *testing.T) {
	controller, err := NewStaticNodeController(&StaticNodeControllerOption{
		Store: storage.NewStaticNodeObjectStore(&fakeControllerStaticNodeObjectStorage{}),
	})
	require.NoError(t, err)

	err = controller.Reconcile(&v1.StaticNodeCluster{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to assert obj to *v1.StaticNode")
}

func TestStaticNodeControllerForceDeleteHardDeletesAfterBestEffortCleanup(t *testing.T) {
	objectStorage := &fakeControllerStaticNodeObjectStorage{}
	node := controllerStaticNode()
	node.Metadata.DeletionTimestamp = "2026-06-15T16:47:17Z"
	node.Metadata.Annotations = map[string]string{
		"neutree.ai/force-delete": "true",
	}
	controller, err := NewStaticNodeController(&StaticNodeControllerOption{
		Store: storage.NewStaticNodeObjectStore(objectStorage),
	})
	require.NoError(t, err)
	controller.newRunner = func(context.Context, *v1.StaticNode) (clusterreconcile.StaticNodeCommandRunner, error) {
		return &fakeControllerStaticNodeRunner{
			responses: []fakeControllerStaticNodeRunnerResponse{
				{err: errors.New("remote cleanup failed")},
			},
		}, nil
	}

	err = controller.Reconcile(node)

	require.NoError(t, err)
	assert.Equal(t, []string{"8"}, objectStorage.deletedIDs)
	assert.Empty(t, objectStorage.updatedStatus)
}

func TestStaticNodeControllerReconcileAlwaysCreatesRunner(t *testing.T) {
	objectStorage := &fakeControllerStaticNodeObjectStorage{}
	node := controllerStaticNode()
	node.Spec.Warm = nil
	node.Spec.Components = nil
	controller, err := NewStaticNodeController(&StaticNodeControllerOption{
		Store: storage.NewStaticNodeObjectStore(objectStorage),
	})
	require.NoError(t, err)

	runnerCreated := false
	controller.newRunner = func(context.Context, *v1.StaticNode) (clusterreconcile.StaticNodeCommandRunner, error) {
		runnerCreated = true

		return &fakeControllerStaticNodeRunner{}, nil
	}

	err = controller.Reconcile(node)

	require.NoError(t, err)
	assert.True(t, runnerCreated)
	statusObj, ok := objectStorage.updatedStatus["8"].(*v1.StaticNode)
	require.True(t, ok)
	require.NotNil(t, statusObj.Status)
	assert.Equal(t, v1.StaticNodePhaseReconciling, statusObj.Status.Phase)
}

type fakeControllerStaticNodeObjectStorage struct {
	updatedStatus map[string]scheme.Object
	deletedIDs    []string
}

func (f *fakeControllerStaticNodeObjectStorage) Create(_ scheme.Object) error {
	return nil
}

func (f *fakeControllerStaticNodeObjectStorage) Update(_ string, _ scheme.Object) error {
	return nil
}

func (f *fakeControllerStaticNodeObjectStorage) Delete(id string, _ scheme.Object) error {
	f.deletedIDs = append(f.deletedIDs, id)

	return nil
}

func (f *fakeControllerStaticNodeObjectStorage) Get(_ string, _ scheme.Object) error {
	return nil
}

func (f *fakeControllerStaticNodeObjectStorage) List(obj scheme.ObjectList, _ storage.ListOption) error {
	obj.SetItems(nil)

	return nil
}

func (f *fakeControllerStaticNodeObjectStorage) UpdateMetadata(_ string, _ scheme.Object) error {
	return nil
}

func (f *fakeControllerStaticNodeObjectStorage) UpdateSpec(_ string, _ scheme.Object) error {
	return nil
}

func (f *fakeControllerStaticNodeObjectStorage) UpdateStatus(id string, data scheme.Object) error {
	if f.updatedStatus == nil {
		f.updatedStatus = map[string]scheme.Object{}
	}

	f.updatedStatus[id] = data

	return nil
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
