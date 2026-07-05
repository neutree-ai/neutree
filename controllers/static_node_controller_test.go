package controllers

import (
	"context"
	"errors"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/cluster/staticnode"
	commandrunner "github.com/neutree-ai/neutree/pkg/command_runner"
	"github.com/neutree-ai/neutree/pkg/storage"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestStaticNodeControllerReconcile(t *testing.T) {
	updatedStatus := map[string]*v1.StaticNode{}
	var updatedStatusHistory []*v1.StaticNode
	runner := &fakeControllerStaticNodeRunner{
		responses: []fakeControllerStaticNodeRunnerResponse{
			{
				command: "mkdir -p '/etc/neutree/docker'",
			},
			{
				command: "docker image inspect --format='{{index .RepoDigests 0}}' 'registry.example.com/neutree/neutree-serve:v1.2.0'",
				output:  "registry.example.com/neutree/neutree-serve@sha256:ready\n",
			},
		},
	}
	controller, err := NewStaticNodeController(&StaticNodeControllerOption{
		Storage: newMockStaticNodeStorage(
			t,
			nil,
			nil,
			func(id string, data *v1.StaticNode) {
				updatedStatus[id] = data
				updatedStatusHistory = append(updatedStatusHistory, data)
			},
			nil,
			nil,
		),
		RunnerFactory: &fakeControllerStaticNodeRunnerFactory{
			runner: runner,
		},
	})
	require.NoError(t, err)

	err = controller.Reconcile(controllerStaticNode())

	require.NoError(t, err)
	statusObj := updatedStatus["8"]
	require.NotNil(t, statusObj)
	require.NotNil(t, statusObj.Status)
	assert.Equal(t, v1.StaticNodePhaseReconciling, statusObj.Status.Phase)
	require.NotNil(t, statusObj.Status.Warm)
	assert.True(t, statusObj.Status.Warm.Ready)
	require.Len(t, updatedStatusHistory, 4)
	assert.NotNil(t, updatedStatusHistory[0].Status.Accelerator)
	assert.Nil(t, updatedStatusHistory[0].Status.Warm)
	require.NotNil(t, updatedStatusHistory[1].Status.Warm)
	assert.True(t, updatedStatusHistory[1].Status.Warm.Ready)
	assert.Equal(t, v1.StaticNodePhaseReconciling, updatedStatusHistory[2].Status.Phase)
	assert.Equal(t, v1.StaticNodePhaseReconciling, updatedStatusHistory[3].Status.Phase)
	assert.Equal(t, 2, runner.calls)
}

func TestFindStaticNodeUsesWorkspaceNameFilters(t *testing.T) {
	mockStorage := storagemocks.NewMockStorage(t)
	expectedFilters := []storage.Filter{
		{Column: "metadata->>workspace", Operator: "eq", Value: "default"},
		{Column: "metadata->>name", Operator: "eq", Value: "head-0"},
	}
	mockStorage.On("ListStaticNode", storage.ListOption{
		Filters: expectedFilters,
	}).Return([]v1.StaticNode{*controllerStaticNode()}, nil)

	node, found, err := findStaticNode(mockStorage, "default", "head-0")

	require.NoError(t, err)
	assert.True(t, found)
	require.NotNil(t, node)
	assert.Equal(t, "head-0", node.Metadata.Name)
	mockStorage.AssertExpectations(t)
}

func TestStaticNodeControllerReconcileUsesParentClusterImageRegistryAuth(t *testing.T) {
	updatedStatus := map[string]*v1.StaticNode{}
	runner := &fakeControllerStaticNodeRunner{
		responses: []fakeControllerStaticNodeRunnerResponse{
			{
				command: "mkdir -p '/etc/neutree/docker'",
			},
			{
				command: "cat '/etc/neutree/docker/config.json'",
				err:     errors.New("not found"),
			},
			{
				command: "docker --config '/etc/neutree/docker' login 'registry.example.com' -u 'user' -p 'token'",
			},
			{
				command: "docker image inspect --format='{{index .RepoDigests 0}}' 'registry.example.com/neutree/neutree-serve:v1.2.0'",
				err:     errors.New("not found"),
			},
			{
				command: "docker --config '/etc/neutree/docker' pull 'registry.example.com/neutree/neutree-serve:v1.2.0'",
			},
			{
				command: "docker image inspect --format='{{index .RepoDigests 0}}' 'registry.example.com/neutree/neutree-serve:v1.2.0'",
				output:  "registry.example.com/neutree/neutree-serve@sha256:ready\n",
			},
		},
	}
	controller, err := NewStaticNodeController(&StaticNodeControllerOption{
		Storage: newMockStaticNodeStorage(
			t,
			[]v1.Cluster{
				{
					Metadata: &v1.Metadata{Workspace: "default", Name: "static-a"},
					Spec:     &v1.ClusterSpec{ImageRegistry: "registry-a"},
				},
			},
			[]v1.ImageRegistry{
				{
					Metadata: &v1.Metadata{Workspace: "default", Name: "registry-a"},
					Spec: &v1.ImageRegistrySpec{
						URL:        "https://registry.example.com",
						Repository: "neutree",
						AuthConfig: v1.ImageRegistryAuthConfig{
							Username: "user",
							Password: "token",
						},
					},
					Status: &v1.ImageRegistryStatus{Phase: v1.ImageRegistryPhaseCONNECTED},
				},
			},
			func(id string, data *v1.StaticNode) {
				updatedStatus[id] = data
			},
			nil,
			nil,
		),
		RunnerFactory: &fakeControllerStaticNodeRunnerFactory{
			runner: runner,
		},
	})
	require.NoError(t, err)

	err = controller.Reconcile(controllerStaticNode())

	require.NoError(t, err)
	assert.Equal(t, 6, runner.calls)
	statusObj := updatedStatus["8"]
	require.NotNil(t, statusObj)
	require.NotNil(t, statusObj.Status)
	require.NotNil(t, statusObj.Status.Warm)
	assert.True(t, statusObj.Status.Warm.Ready)
}

func TestStaticNodeControllerReconcileWritesNodeDeviceSnapshot(t *testing.T) {
	updatedStatus := map[string]*v1.StaticNode{}
	var updatedStatusHistory []*v1.StaticNode
	node := controllerStaticNode()
	node.Spec.Warm = nil
	node.Spec.Components = []v1.NodeComponentSpec{
		{
			Name:  "neutree-node-agent",
			Image: "registry.example.com/neutree-node-agent:v1.2.0",
		},
	}
	runner := &fakeControllerStaticNodeRunner{
		responses: []fakeControllerStaticNodeRunnerResponse{
			{
				command: "mkdir -p '/etc/neutree/docker'",
			},
			{
				output: "",
			},
			{},
			{},
			{},
		},
	}
	controller, err := NewStaticNodeController(&StaticNodeControllerOption{
		Storage: newMockStaticNodeStorage(
			t,
			nil,
			nil,
			func(id string, data *v1.StaticNode) {
				updatedStatus[id] = data
				updatedStatusHistory = append(updatedStatusHistory, data)
			},
			nil,
			nil,
		),
		RunnerFactory: &fakeControllerStaticNodeRunnerFactory{
			runner: runner,
		},
		Reconciler: &staticnode.Reconciler{
			NodeDeviceSnapshotClient: &fakeControllerNodeDeviceSnapshotClient{
				snapshot: &staticnode.NodeDeviceSnapshot{
					Accelerator: v1.StaticNodeAcceleratorStatus{
						Type: v1.AcceleratorTypeNVIDIAGPU.String(),
						Devices: []v1.StaticNodeAcceleratorDeviceStatus{
							{UUID: "GPU-abc", ProductName: "NVIDIA A100"},
						},
					},
					Allocations: []v1.StaticNodeAllocationStatus{
						{Endpoint: "chat", ReplicaID: "replica-a"},
					},
				},
			},
		},
	})
	require.NoError(t, err)

	err = controller.Reconcile(node)

	require.NoError(t, err)
	statusObj := updatedStatus["8"]
	require.NotNil(t, statusObj)
	require.NotNil(t, statusObj.Status)
	require.NotNil(t, statusObj.Status.Accelerator)
	require.Len(t, statusObj.Status.Accelerator.Devices, 1)
	assert.Equal(t, "GPU-abc", statusObj.Status.Accelerator.Devices[0].UUID)
	require.Len(t, statusObj.Status.Allocations, 1)
	assert.Equal(t, "chat", statusObj.Status.Allocations[0].Endpoint)
	assert.Equal(t, "replica-a", statusObj.Status.Allocations[0].ReplicaID)
	require.Len(t, updatedStatusHistory, 4)
	assert.Empty(t, updatedStatusHistory[2].Status.Allocations)
	require.Len(t, updatedStatusHistory[3].Status.Allocations, 1)
}

func TestStaticNodeControllerUpdateStatusDoesNotWriteParentStaticNodeCluster(t *testing.T) {
	updatedStaticNodeClusterStatus := map[string]*v1.StaticNodeCluster{}
	controller, err := NewStaticNodeController(&StaticNodeControllerOption{
		Storage: newMockStaticNodeStorage(
			t,
			nil,
			nil,
			nil,
			func(id string, data *v1.StaticNodeCluster) {
				updatedStaticNodeClusterStatus[id] = data
			},
			nil,
		),
	})
	require.NoError(t, err)
	reconcileErr := error(nil)
	node := controllerStaticNode()

	controller.updateStatus(node, v1.StaticNodeStatus{
		Phase: v1.StaticNodePhaseReady,
		Warm:  &v1.WarmStatus{Ready: true},
	}, "failed to update static node status", &reconcileErr)

	require.NoError(t, reconcileErr)
	assert.Empty(t, updatedStaticNodeClusterStatus)
}

func TestStaticNodeControllerReconcileRejectsWrongType(t *testing.T) {
	controller, err := NewStaticNodeController(&StaticNodeControllerOption{
		Storage: newMockStaticNodeStorage(t, nil, nil, nil, nil, nil),
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
	updatedStatus := map[string]*v1.StaticNode{}
	var deletedIDs []string
	node := controllerStaticNode()
	node.Metadata.DeletionTimestamp = "2026-06-15T16:47:17Z"
	node.Metadata.Annotations = map[string]string{
		"neutree.ai/force-delete": "true",
	}
	controller, err := NewStaticNodeController(&StaticNodeControllerOption{
		Storage: newMockStaticNodeStorage(
			t,
			nil,
			nil,
			func(id string, data *v1.StaticNode) {
				updatedStatus[id] = data
			},
			nil,
			func(id string) {
				deletedIDs = append(deletedIDs, id)
			},
		),
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
	assert.Equal(t, []string{"8"}, deletedIDs)
	assert.Empty(t, updatedStatus)
}

func TestStaticNodeControllerDeleteFailureUpdatesStatusWithoutHardDelete(t *testing.T) {
	updatedStatus := map[string]*v1.StaticNode{}
	var deletedIDs []string
	node := controllerStaticNode()
	node.Metadata.DeletionTimestamp = "2026-06-15T16:47:17Z"
	controller, err := NewStaticNodeController(&StaticNodeControllerOption{
		Storage: newMockStaticNodeStorage(
			t,
			nil,
			nil,
			func(id string, data *v1.StaticNode) {
				updatedStatus[id] = data
			},
			nil,
			func(id string) {
				deletedIDs = append(deletedIDs, id)
			},
		),
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
	assert.Empty(t, deletedIDs)
	statusObj := updatedStatus["8"]
	require.NotNil(t, statusObj)
	require.NotNil(t, statusObj.Status)
	assert.Equal(t, v1.StaticNodePhaseFailed, statusObj.Status.Phase)
	assert.Contains(t, statusObj.Status.ErrorMessage, "remote cleanup failed")
}

func TestStaticNodeControllerReconcileAlwaysCreatesRunner(t *testing.T) {
	updatedStatus := map[string]*v1.StaticNode{}
	node := controllerStaticNode()
	node.Spec.Warm = nil
	node.Spec.Components = nil
	runnerCreated := false
	controller, err := NewStaticNodeController(&StaticNodeControllerOption{
		Storage: newMockStaticNodeStorage(
			t,
			nil,
			nil,
			func(id string, data *v1.StaticNode) {
				updatedStatus[id] = data
			},
			nil,
			nil,
		),
		RunnerFactory: &fakeControllerStaticNodeRunnerFactory{
			buildRunner: func(context.Context, *v1.StaticNode) (staticnode.CommandRunner, error) {
				runnerCreated = true

				return &fakeControllerStaticNodeRunner{
					responses: []fakeControllerStaticNodeRunnerResponse{
						{command: "mkdir -p '/etc/neutree/docker'"},
					},
				}, nil
			},
		},
	})
	require.NoError(t, err)

	err = controller.Reconcile(node)

	require.NoError(t, err)
	assert.True(t, runnerCreated)
	statusObj := updatedStatus["8"]
	require.NotNil(t, statusObj)
	require.NotNil(t, statusObj.Status)
	assert.Equal(t, v1.StaticNodePhaseReconciling, statusObj.Status.Phase)
}

func newMockStaticNodeStorage(
	t *testing.T,
	clusters []v1.Cluster,
	imageRegistries []v1.ImageRegistry,
	onUpdateStaticNode func(string, *v1.StaticNode),
	onUpdateStaticNodeCluster func(string, *v1.StaticNodeCluster),
	onDeleteStaticNode func(string),
) *storagemocks.MockStorage {
	t.Helper()

	mockStorage := storagemocks.NewMockStorage(t)
	mockStorage.On("ListCluster", mock.Anything).Return(clusters, nil).Maybe()
	mockStorage.On("ListImageRegistry", mock.Anything).Return(imageRegistries, nil).Maybe()
	mockStorage.On("UpdateStaticNode", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			if onUpdateStaticNode != nil {
				onUpdateStaticNode(args.Get(0).(string), args.Get(1).(*v1.StaticNode))
			}
		}).
		Return(nil).
		Maybe()
	mockStorage.On("UpdateStaticNodeCluster", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			if onUpdateStaticNodeCluster != nil {
				onUpdateStaticNodeCluster(args.Get(0).(string), args.Get(1).(*v1.StaticNodeCluster))
			}
		}).
		Return(nil).
		Maybe()
	mockStorage.On("DeleteStaticNode", mock.Anything).
		Run(func(args mock.Arguments) {
			if onDeleteStaticNode != nil {
				onDeleteStaticNode(args.Get(0).(string))
			}
		}).
		Return(nil).
		Maybe()

	return mockStorage
}

type fakeControllerStaticNodeRunnerFactory struct {
	runner      staticnode.CommandRunner
	buildRunner func(context.Context, *v1.StaticNode) (staticnode.CommandRunner, error)
}

func (f *fakeControllerStaticNodeRunnerFactory) NewStaticNodeRunner(
	ctx context.Context,
	node *v1.StaticNode,
) (staticnode.CommandRunner, error) {
	if f.buildRunner != nil {
		return f.buildRunner(ctx, node)
	}

	return f.runner, nil
}

type fakeControllerStaticNodeRunner struct {
	responses []fakeControllerStaticNodeRunnerResponse
	calls     int
}

var _ staticnode.CommandRunner = (*fakeControllerStaticNodeRunner)(nil)

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

type fakeControllerNodeDeviceSnapshotClient struct {
	snapshot *staticnode.NodeDeviceSnapshot
	err      error
}

func (f *fakeControllerNodeDeviceSnapshotClient) DeviceSnapshot(
	_ context.Context,
	_ *v1.StaticNode,
) (*staticnode.NodeDeviceSnapshot, error) {
	return f.snapshot, f.err
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
