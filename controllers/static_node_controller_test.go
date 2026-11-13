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
	node := controllerStaticNode()
	node.Status = &v1.StaticNodeStatus{Phase: v1.StaticNodePhaseFailed, ErrorMessage: "previous discovery failed"}
	var updatedStatusHistory []*v1.StaticNode
	runner := &fakeControllerStaticNodeRunner{
		beforeRun: func(string) {
			require.NotNil(t, updatedStatus["8"])
			assert.Equal(t, "previous discovery failed", updatedStatus["8"].Status.ErrorMessage)
		},
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

	err = controller.Reconcile(node)

	require.NoError(t, err)
	statusObj := updatedStatus["8"]
	require.NotNil(t, statusObj)
	require.NotNil(t, statusObj.Status)
	assert.Equal(t, v1.StaticNodePhaseReconciling, statusObj.Status.Phase)
	require.NotNil(t, statusObj.Status.Warm)
	assert.True(t, statusObj.Status.Warm.Ready)
	require.Len(t, updatedStatusHistory, 2)
	assert.NotNil(t, updatedStatusHistory[0].Status.Accelerator)
	assert.Nil(t, updatedStatusHistory[0].Status.Warm)
	assert.Empty(t, statusObj.Status.ErrorMessage)
	assert.Equal(t, 2, runner.calls)
}

func TestStaticNodeControllerKeepsErrorUntilReconcileReturns(t *testing.T) {
	const previousError = "previous component startup failure"
	for _, tc := range []struct {
		name           string
		warmError      error
		componentError error
		healthError    error
	}{
		{name: "warm failure replaces previous error", warmError: errors.New("x509: certificate signed by unknown authority")},
		{name: "component failure replaces previous error", componentError: errors.New("port is already allocated")},
		{name: "successful retry clears error after snapshot"},
		{name: "health check waiting clears old error only after completion", healthError: errors.New("node agent is not healthy")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := controllerStaticNode()
			component := v1.NodeComponentSpec{
				Name: "neutree-node-agent", Image: node.Spec.Warm.Images[0].Ref, ConfigHash: "hash-agent",
			}
			if tc.healthError != nil {
				component.HealthCheck = &v1.NodeComponentHealthCheck{Command: []string{"check-node-agent"}}
			}
			node.Spec.Components = []v1.NodeComponentSpec{component}
			accelerator := &v1.StaticNodeAcceleratorStatus{Type: v1.AcceleratorTypeNVIDIAGPU.String()}
			node.Status = &v1.StaticNodeStatus{
				Phase: v1.StaticNodePhaseFailed, ErrorMessage: previousError,
				Accelerator: accelerator,
				Warm:        &v1.WarmStatus{Ready: true},
				Components:  []v1.NodeComponentStatus{{Name: component.Name, Phase: v1.NodeComponentPhaseFailed}},
			}

			persisted := *node.Status
			var history []v1.StaticNodeStatus
			checkInProgress := func() {
				assert.Equal(t, previousError, persisted.ErrorMessage)
				assert.Equal(t, v1.StaticNodePhaseFailed, persisted.Phase)
			}
			snapshotCalled := false
			runner := &fakeControllerStaticNodeRunner{
				beforeRun: func(string) { checkInProgress() },
				responses: []fakeControllerStaticNodeRunnerResponse{
					{command: "mkdir -p '/etc/neutree/docker'"},
					{command: "docker image inspect --format='{{index .RepoDigests 0}}' '" + component.Image + "'", output: "sha256:ready"},
					{command: "mkdir -p '/etc/neutree/docker'"},
					{command: "docker inspect --format='{{index .Config.Labels \"neutree.ai/component-hash\"}} {{.State.Running}}' 'neutree-static-a-neutree-node-agent'", output: "hash-agent true"},
				},
			}
			if tc.warmError != nil {
				runner.responses = runner.responses[:2]
				runner.responses[1].err = errors.New("image not found")
				runner.responses = append(runner.responses, fakeControllerStaticNodeRunnerResponse{
					command: "docker pull '" + component.Image + "'", err: tc.warmError,
				})
			} else if tc.componentError != nil {
				runner.responses[3].output = ""
				runner.responses = append(runner.responses,
					fakeControllerStaticNodeRunnerResponse{command: "docker image inspect '" + component.Image + "' >/dev/null"},
					fakeControllerStaticNodeRunnerResponse{command: "docker rm -f 'neutree-static-a-neutree-node-agent' >/dev/null 2>&1 || true"},
					fakeControllerStaticNodeRunnerResponse{err: tc.componentError},
				)
			}
			if tc.healthError != nil {
				runner.responses = append(runner.responses, fakeControllerStaticNodeRunnerResponse{command: "'check-node-agent'", err: tc.healthError})
			}
			controller, err := NewStaticNodeController(&StaticNodeControllerOption{
				Storage: newMockStaticNodeStorage(t, nil, nil, func(_ string, data *v1.StaticNode) {
					persisted = *data.Status
					history = append(history, persisted)
				}, nil, nil),
				RunnerFactory: &fakeControllerStaticNodeRunnerFactory{runner: runner},
				Reconciler: &staticnode.Reconciler{
					NodeDeviceSnapshotClient: &fakeControllerNodeDeviceSnapshotClient{
						beforeSnapshot: func() {
							checkInProgress()
							snapshotCalled = true
						},
						snapshot: &v1.NodeDeviceSnapshot{Accelerator: *accelerator},
					},
				},
			})
			require.NoError(t, err)

			err = controller.Reconcile(node)

			if tc.warmError != nil || tc.componentError != nil {
				cause := tc.warmError
				if cause == nil {
					cause = tc.componentError
				}
				require.ErrorContains(t, err, cause.Error())
				assert.Contains(t, persisted.ErrorMessage, cause.Error())
				assert.Equal(t, v1.StaticNodePhaseFailed, persisted.Phase)
				assert.False(t, snapshotCalled)
			} else if tc.healthError != nil {
				require.NoError(t, err)
				assert.Equal(t, v1.StaticNodePhaseReconciling, persisted.Phase)
				assert.Empty(t, persisted.ErrorMessage)
				require.Len(t, persisted.Components, 1)
				assert.Equal(t, "HealthCheckFailed", persisted.Components[0].Reason)
				assert.Equal(t, tc.healthError.Error(), persisted.Components[0].Message)
				assert.False(t, snapshotCalled)
			} else {
				require.NoError(t, err)
				assert.True(t, snapshotCalled)
				assert.Equal(t, v1.StaticNodePhaseReady, persisted.Phase)
				assert.Empty(t, persisted.ErrorMessage)
			}
			for _, status := range history[:len(history)-1] {
				assert.Equal(t, previousError, status.ErrorMessage)
			}
		})
	}
}

func TestStaticNodeControllerReturnsStatusWriteErrors(t *testing.T) {
	for _, tc := range []struct {
		name      string
		discover  bool
		wantError string
	}{
		{name: "discovery publication", discover: true, wantError: "failed to update static node accelerator status"},
		{name: "final status", wantError: "failed to update static node status"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := controllerStaticNode()
			if !tc.discover {
				node.Status = &v1.StaticNodeStatus{Accelerator: &v1.StaticNodeAcceleratorStatus{Type: v1.StaticNodeAcceleratorTypeCPU}}
			}
			runner := &fakeControllerStaticNodeRunner{responses: []fakeControllerStaticNodeRunnerResponse{
				{command: "mkdir -p '/etc/neutree/docker'"},
				{output: "sha256:ready"},
			}}
			store := storagemocks.NewMockStorage(t)
			store.On("UpdateStaticNode", "8", mock.Anything).Return(assert.AnError).Once()
			if !tc.discover {
				store.On("ListCluster", mock.Anything).Return([]v1.Cluster{}, nil)
			}
			controller, err := NewStaticNodeController(&StaticNodeControllerOption{
				Storage:       store,
				RunnerFactory: &fakeControllerStaticNodeRunnerFactory{runner: runner},
			})
			require.NoError(t, err)

			err = controller.Reconcile(node)

			require.ErrorIs(t, err, assert.AnError)
			assert.ErrorContains(t, err, tc.wantError)
			if tc.discover {
				assert.Zero(t, runner.calls, "failed discovery publication must block Docker operations")
			} else {
				assert.Equal(t, len(runner.responses), runner.calls)
			}
		})
	}
}

func TestStaticNodeControllerRecordsAcceleratorDiscoveryFailure(t *testing.T) {
	cause := errors.New("accelerator discovery failed")
	node := controllerStaticNode()
	node.Spec.SSHAuth = &v1.Auth{}
	runner := &fakeControllerStaticNodeRunner{}
	var status *v1.StaticNodeStatus
	store := storagemocks.NewMockStorage(t)
	store.On("UpdateStaticNode", "8", mock.Anything).Run(func(args mock.Arguments) {
		status = args.Get(1).(*v1.StaticNode).Status
	}).Return(nil).Once()
	controller, err := NewStaticNodeController(&StaticNodeControllerOption{
		Storage:       store,
		RunnerFactory: &fakeControllerStaticNodeRunnerFactory{runner: runner},
		Reconciler:    &staticnode.Reconciler{AcceleratorManager: fakeControllerAcceleratorManager{err: cause}},
	})
	require.NoError(t, err)

	err = controller.Reconcile(node)

	require.ErrorIs(t, err, cause)
	require.NotNil(t, status)
	assert.Equal(t, v1.StaticNodePhaseFailed, status.Phase)
	assert.Contains(t, status.ErrorMessage, cause.Error())
	assert.Zero(t, runner.calls)
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
	node.Spec.SSHAuth = &v1.Auth{}
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
			AcceleratorManager: fakeControllerAcceleratorManager{
				accelerator: &v1.StaticNodeAcceleratorStatus{
					Type: v1.AcceleratorTypeNVIDIAGPU.String(),
				},
			},
			NodeDeviceSnapshotClient: &fakeControllerNodeDeviceSnapshotClient{
				snapshot: &v1.NodeDeviceSnapshot{
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
	require.Len(t, updatedStatusHistory, 2)
	assert.Empty(t, updatedStatusHistory[0].Status.Allocations)
	require.Len(t, updatedStatusHistory[1].Status.Allocations, 1)
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

func TestStaticNodeControllerUpdateStatusSkipsUnchangedStatus(t *testing.T) {
	updateCount := 0
	controller, err := NewStaticNodeController(&StaticNodeControllerOption{
		Storage: newMockStaticNodeStorage(
			t,
			nil,
			nil,
			func(string, *v1.StaticNode) {
				updateCount++
			},
			nil,
			nil,
		),
	})
	require.NoError(t, err)
	reconcileErr := error(nil)
	node := controllerStaticNode()
	node.Status = &v1.StaticNodeStatus{
		Phase: v1.StaticNodePhaseReady,
		Accelerator: &v1.StaticNodeAcceleratorStatus{
			Type: v1.AcceleratorTypeNVIDIAGPU.String(),
			Devices: []v1.StaticNodeAcceleratorDeviceStatus{
				{UUID: "GPU-abc", ProductModel: "NVIDIA_Tesla_T4"},
			},
		},
		Warm: &v1.WarmStatus{Ready: true},
	}

	controller.updateStatus(node, *node.Status, "failed to update static node status", &reconcileErr)

	require.NoError(t, reconcileErr)
	assert.Equal(t, 0, updateCount)
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
	beforeRun func(string)
}

var _ staticnode.CommandRunner = (*fakeControllerStaticNodeRunner)(nil)

type fakeControllerStaticNodeRunnerResponse struct {
	command string
	output  string
	err     error
}

func (f *fakeControllerStaticNodeRunner) Run(_ context.Context, command string) (string, error) {
	if f.beforeRun != nil {
		f.beforeRun(command)
	}
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

type fakeControllerAcceleratorManager struct {
	accelerator *v1.StaticNodeAcceleratorStatus
	err         error
}

func (f fakeControllerAcceleratorManager) DetectAccelerator(
	_ context.Context,
	_ string,
	_ v1.Auth,
) (*v1.StaticNodeAcceleratorStatus, error) {
	return f.accelerator, f.err
}

type fakeControllerNodeDeviceSnapshotClient struct {
	snapshot       *v1.NodeDeviceSnapshot
	err            error
	beforeSnapshot func()
}

func (f *fakeControllerNodeDeviceSnapshotClient) DeviceSnapshot(
	_ context.Context,
	_ *v1.StaticNode,
) (*v1.NodeDeviceSnapshot, error) {
	if f.beforeSnapshot != nil {
		f.beforeSnapshot()
	}
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
