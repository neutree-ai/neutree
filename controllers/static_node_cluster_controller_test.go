package controllers

import (
	"errors"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
	dashboardmocks "github.com/neutree-ai/neutree/internal/ray/dashboard/mocks"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestStaticNodeClusterControllerReconcile(t *testing.T) {
	var created []*v1.StaticNode
	updatedStatus := map[string]*v1.StaticNodeCluster{}
	mockStorage := newMockStaticNodeClusterStorage(t, nil)
	mockStorage.On("CreateStaticNode", mock.Anything).
		Run(func(args mock.Arguments) {
			created = append(created, args.Get(0).(*v1.StaticNode))
		}).
		Return(nil).
		Maybe()
	mockStorage.On("UpdateStaticNodeCluster", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			data := args.Get(1).(*v1.StaticNodeCluster)
			if data != nil && data.Status != nil {
				updatedStatus[args.Get(0).(string)] = data
			}
		}).
		Return(nil).
		Maybe()
	controller, err := NewStaticNodeClusterController(&StaticNodeClusterControllerOption{
		Storage: mockStorage,
	})
	require.NoError(t, err)

	err = controller.Reconcile(controllerStaticNodeCluster())

	require.NoError(t, err)
	require.Len(t, created, 2)
	assert.Equal(t, "head-0", created[0].Metadata.Name)
	assert.Equal(t, "worker-0", created[1].Metadata.Name)
	status := updatedStatus["7"]
	require.NotNil(t, status)
	require.NotNil(t, status.Status)
	assert.Equal(t, v1.StaticNodeClusterPhaseProvisioning, status.Status.Phase)
}

func TestStaticNodeClusterControllerReconcileRejectsWrongType(t *testing.T) {
	controller, err := NewStaticNodeClusterController(&StaticNodeClusterControllerOption{
		Storage: newMockStaticNodeClusterStorage(t, nil),
	})
	require.NoError(t, err)

	err = controller.Reconcile(&v1.StaticNode{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to assert obj to *v1.StaticNodeCluster")
}

func TestNewStaticNodeClusterControllerRequiresStorage(t *testing.T) {
	controller, err := NewStaticNodeClusterController(&StaticNodeClusterControllerOption{})

	require.Error(t, err)
	assert.Nil(t, controller)
	assert.Contains(t, err.Error(), "storage is required")
}

func TestStaticNodeClusterControllerReconcileRecordsNodeOwnerConflict(t *testing.T) {
	updatedStatus := map[string]*v1.StaticNodeCluster{}
	mockStorage := newMockStaticNodeClusterStorage(t, []v1.StaticNode{
		{
			ID: 11,
			Metadata: &v1.Metadata{
				Workspace: "default",
				Name:      "head-0",
			},
			Spec: &v1.StaticNodeSpec{
				Cluster: "static-a",
			},
		},
	})
	mockStorage.On("UpdateStaticNodeCluster", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			data := args.Get(1).(*v1.StaticNodeCluster)
			if data != nil && data.Status != nil {
				updatedStatus[args.Get(0).(string)] = data
			}
		}).
		Return(nil).
		Maybe()
	controller, err := NewStaticNodeClusterController(&StaticNodeClusterControllerOption{
		Storage: mockStorage,
	})
	require.NoError(t, err)

	cluster := controllerStaticNodeCluster()
	cluster.Metadata.Name = "static-b"
	cluster.Spec.Nodes = []v1.StaticNodeClusterNodeSpec{
		{
			Name: "head-0",
			IP:   "10.0.0.10",
			Role: v1.StaticNodeRoleHead,
		},
	}

	err = controller.Reconcile(cluster)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "already owned by static node cluster static-a")
	status := updatedStatus["7"]
	require.NotNil(t, status)
	require.NotNil(t, status.Status)
	assert.Equal(t, v1.StaticNodeClusterPhaseFailed, status.Status.Phase)
	assert.Contains(t, status.Status.ErrorMessage, "already owned by static node cluster static-a")
}

func TestStaticNodeClusterControllerReconcileWaitsForStaleNodeDeletion(t *testing.T) {
	updatedMetadata := map[string]*v1.StaticNode{}
	updatedStatus := map[string]*v1.StaticNodeCluster{}
	mockStorage := newMockStaticNodeClusterStorage(t, []v1.StaticNode{
		controllerStaticClusterNode("head-0", v1.StaticNodeRoleHead, v1.StaticNodePhaseReady),
		controllerStaticClusterNode("worker-0", v1.StaticNodeRoleWorker, v1.StaticNodePhaseReady),
		controllerStaticClusterNode("worker-stale", v1.StaticNodeRoleWorker, v1.StaticNodePhaseReady),
	})
	mockStorage.On("UpdateStaticNode", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			data := args.Get(1).(*v1.StaticNode)
			if data != nil && data.Metadata != nil {
				updatedMetadata[args.Get(0).(string)] = data
			}
		}).
		Return(nil).
		Maybe()
	mockStorage.On("UpdateStaticNodeCluster", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			data := args.Get(1).(*v1.StaticNodeCluster)
			if data != nil && data.Status != nil {
				updatedStatus[args.Get(0).(string)] = data
			}
		}).
		Return(nil).
		Maybe()
	controller, err := NewStaticNodeClusterController(&StaticNodeClusterControllerOption{
		Storage: mockStorage,
	})
	require.NoError(t, err)

	err = controller.Reconcile(controllerStaticNodeCluster())

	require.NoError(t, err)
	status := updatedStatus["7"]
	require.NotNil(t, status)
	require.NotNil(t, status.Status)
	assert.Equal(t, v1.StaticNodeClusterPhaseProvisioning, status.Status.Phase)
	assert.Contains(t, status.Status.ErrorMessage, "stale static node worker-stale is deleting")
	assert.Contains(t, updatedMetadata, "13")
}

func TestStaticNodeClusterControllerReconcileRequiresRayVerificationBeforeReady(t *testing.T) {
	updatedStatus := map[string]*v1.StaticNodeCluster{}
	nodes := []v1.StaticNode{
		controllerStaticClusterNode("head-0", v1.StaticNodeRoleHead, v1.StaticNodePhaseReady),
		controllerStaticClusterNode("worker-0", v1.StaticNodeRoleWorker, v1.StaticNodePhaseReady),
	}
	mockStorage := newMockStaticNodeClusterStorage(t, nodes)
	mockStaticNodeUpdatesPreservingStatus(mockStorage, nodes)
	mockStorage.On("UpdateStaticNodeCluster", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			data := args.Get(1).(*v1.StaticNodeCluster)
			if data != nil && data.Status != nil {
				updatedStatus[args.Get(0).(string)] = data
			}
		}).
		Return(nil).
		Maybe()
	mockDashboard := mockStaticNodeClusterDashboard(t)
	mockDashboard.On("ListNodes").Return(nil, errors.New("connection refused")).Once()
	controller, err := NewStaticNodeClusterController(&StaticNodeClusterControllerOption{
		Storage: mockStorage,
	})
	require.NoError(t, err)

	err = controller.Reconcile(controllerStaticNodeCluster())

	require.NoError(t, err)
	status := updatedStatus["7"]
	require.NotNil(t, status)
	require.NotNil(t, status.Status)
	assert.Equal(t, v1.StaticNodeClusterPhaseProvisioning, status.Status.Phase)
	assert.Contains(t, status.Status.ErrorMessage, "ray cluster verification failed")
	assert.Contains(t, status.Status.ErrorMessage, "connection refused")
}

func TestStaticNodeClusterControllerReconcileDoesNotUpdateStaticNodeStatusOnUpsert(t *testing.T) {
	nodes := []v1.StaticNode{
		controllerStaticClusterNode("head-0", v1.StaticNodeRoleHead, v1.StaticNodePhaseReconciling),
		controllerStaticClusterNode("worker-0", v1.StaticNodeRoleWorker, v1.StaticNodePhaseReconciling),
	}
	nodes[0].Status.Accelerator = &v1.StaticNodeAcceleratorStatus{
		Type: v1.StaticNodeAcceleratorTypeCPU,
		Devices: []v1.StaticNodeAcceleratorDeviceStatus{
			{UUID: "GPU-head", ProductModel: "NVIDIA_Tesla_T4"},
		},
	}

	updatedNodes := map[string]*v1.StaticNode{}
	mockStorage := newMockStaticNodeClusterStorage(t, nodes)
	mockStorage.On("UpdateStaticNode", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			updatedNodes[args.Get(0).(string)] = args.Get(1).(*v1.StaticNode)
		}).
		Return(nil).
		Maybe()
	mockStorage.On("UpdateStaticNodeCluster", mock.Anything, mock.Anything).Return(nil).Maybe()
	controller, err := NewStaticNodeClusterController(&StaticNodeClusterControllerOption{
		Storage: mockStorage,
	})
	require.NoError(t, err)

	err = controller.Reconcile(controllerStaticNodeCluster())

	require.NoError(t, err)
	updated := updatedNodes["11"]
	require.NotNil(t, updated)
	assert.Nil(t, updated.Status)
	require.NotNil(t, updated.Spec)
	assert.Equal(t, "static-a", updated.Spec.Cluster)
}

func TestStaticNodeClusterControllerReconcileFailsReadyClusterWhenRayVerificationFails(t *testing.T) {
	updatedStatus := map[string]*v1.StaticNodeCluster{}
	nodes := []v1.StaticNode{
		controllerStaticClusterNode("head-0", v1.StaticNodeRoleHead, v1.StaticNodePhaseReady),
		controllerStaticClusterNode("worker-0", v1.StaticNodeRoleWorker, v1.StaticNodePhaseReady),
	}
	mockStorage := newMockStaticNodeClusterStorage(t, nodes)
	mockStaticNodeUpdatesPreservingStatus(mockStorage, nodes)
	mockStorage.On("UpdateStaticNodeCluster", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			data := args.Get(1).(*v1.StaticNodeCluster)
			if data != nil && data.Status != nil {
				updatedStatus[args.Get(0).(string)] = data
			}
		}).
		Return(nil).
		Maybe()
	mockDashboard := mockStaticNodeClusterDashboard(t)
	mockDashboard.On("ListNodes").Return(nil, errors.New("connection refused")).Once()
	controller, err := NewStaticNodeClusterController(&StaticNodeClusterControllerOption{
		Storage: mockStorage,
	})
	require.NoError(t, err)

	cluster := controllerStaticNodeCluster()
	cluster.Status = &v1.StaticNodeClusterStatus{Phase: v1.StaticNodeClusterPhaseReady}
	err = controller.Reconcile(cluster)

	require.NoError(t, err)
	status := updatedStatus["7"]
	require.NotNil(t, status)
	require.NotNil(t, status.Status)
	assert.Equal(t, v1.StaticNodeClusterPhaseFailed, status.Status.Phase)
	assert.Contains(t, status.Status.ErrorMessage, "ray cluster verification failed")
}

func TestStaticNodeClusterControllerDeletePropagatesForceDeleteToNodes(t *testing.T) {
	updatedMetadata := map[string]*v1.StaticNode{}
	mockStorage := newMockStaticNodeClusterStorage(t, []v1.StaticNode{
		controllerStaticClusterNode("head-0", v1.StaticNodeRoleHead, v1.StaticNodePhaseReady),
		func() v1.StaticNode {
			node := controllerStaticClusterNode("worker-0", v1.StaticNodeRoleWorker, v1.StaticNodePhaseReady)
			node.Metadata.DeletionTimestamp = "2026-06-30T00:00:00Z"

			return node
		}(),
	})
	mockStorage.On("UpdateStaticNode", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			data := args.Get(1).(*v1.StaticNode)
			if data != nil && data.Metadata != nil {
				updatedMetadata[args.Get(0).(string)] = data
			}
		}).
		Return(nil).
		Maybe()
	mockStorage.On("UpdateStaticNodeCluster", mock.Anything, mock.Anything).Return(nil).Maybe()
	controller, err := NewStaticNodeClusterController(&StaticNodeClusterControllerOption{
		Storage: mockStorage,
	})
	require.NoError(t, err)

	cluster := controllerStaticNodeCluster()
	cluster.Metadata.DeletionTimestamp = "2026-06-30T00:00:00Z"
	cluster.Metadata.Annotations = map[string]string{"neutree.ai/force-delete": "true"}

	err = controller.Reconcile(cluster)

	require.NoError(t, err)
	for _, id := range []string{"11", "12"} {
		updated := updatedMetadata[id]
		require.NotNil(t, updated)
		require.NotNil(t, updated.Metadata)
		assert.True(t, v1.IsForceDelete(updated.Metadata.Annotations))
		assert.NotEmpty(t, updated.Metadata.DeletionTimestamp)
		assert.Nil(t, updated.Spec)
		assert.Nil(t, updated.Status)
	}
}

func newMockStaticNodeClusterStorage(t *testing.T, nodes []v1.StaticNode) *storagemocks.MockStorage {
	t.Helper()

	mockStorage := storagemocks.NewMockStorage(t)
	mockStorage.On("ListStaticNode", mock.Anything).Return(nodes, nil).Maybe()
	mockStorage.On("DeleteStaticNode", mock.Anything).Return(nil).Maybe()
	mockStorage.On("DeleteStaticNodeCluster", mock.Anything).Return(nil).Maybe()

	return mockStorage
}

func mockStaticNodeClusterDashboard(t *testing.T) *dashboardmocks.MockDashboardService {
	t.Helper()

	mockDashboard := dashboardmocks.NewMockDashboardService(t)
	prevFactory := dashboard.NewDashboardService
	dashboard.NewDashboardService = func(string) dashboard.DashboardService {
		return mockDashboard
	}
	t.Cleanup(func() {
		dashboard.NewDashboardService = prevFactory
	})

	return mockDashboard
}

func mockStaticNodeUpdatesPreservingStatus(mockStorage *storagemocks.MockStorage, nodes []v1.StaticNode) {
	mockStorage.On("UpdateStaticNode", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			updated := args.Get(1).(*v1.StaticNode)
			for i := range nodes {
				if nodes[i].ID != updated.ID {
					continue
				}

				status := nodes[i].Status
				nodes[i] = *updated
				nodes[i].Status = status

				return
			}
		}).
		Return(nil).
		Maybe()
}

func controllerStaticNodeCluster() *v1.StaticNodeCluster {
	return &v1.StaticNodeCluster{
		ID: 7,
		Metadata: &v1.Metadata{
			Workspace: "default",
			Name:      "static-a",
		},
		Spec: &v1.StaticNodeClusterSpec{
			Version:       "v1.2.0",
			ImageRegistry: "registry.example.com/neutree",
			Nodes: []v1.StaticNodeClusterNodeSpec{
				{
					Name: "head-0",
					IP:   "10.0.0.10",
					Role: v1.StaticNodeRoleHead,
				},
				{
					Name: "worker-0",
					IP:   "10.0.0.11",
					Role: v1.StaticNodeRoleWorker,
				},
			},
		},
	}
}

func controllerStaticClusterNode(name string, role v1.StaticNodeRole, phase v1.StaticNodePhase) v1.StaticNode {
	id := 11
	switch name {
	case "worker-0":
		id = 12
	case "worker-stale":
		id = 13
	}

	return v1.StaticNode{
		ID: id,
		Metadata: &v1.Metadata{
			Workspace: "default",
			Name:      name,
			Labels: map[string]string{
				"neutree.ai/static-node-cluster": "static-a",
				"neutree.ai/static-node-role":    string(role),
			},
		},
		Spec: &v1.StaticNodeSpec{
			Cluster: "static-a",
			IP:      controllerStaticClusterNodeIP(name),
			Role:    role,
			Warm:    &v1.WarmSpec{},
		},
		Status: &v1.StaticNodeStatus{
			Phase: phase,
			Warm:  &v1.WarmStatus{Ready: true},
		},
	}
}

func controllerStaticClusterNodeIP(name string) string {
	if name == "worker-0" {
		return "10.0.0.11"
	}

	return "10.0.0.10"
}
