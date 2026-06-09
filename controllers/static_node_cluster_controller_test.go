package controllers

import (
	"context"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	clusterreconcile "github.com/neutree-ai/neutree/internal/cluster"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStaticNodeClusterControllerReconcile(t *testing.T) {
	store := &fakeControllerStaticNodeClusterStore{}
	profileProvider := &fakeAcceleratorProfileProvider{
		profiles: map[string]*v1.AcceleratorProfile{
			v1.AcceleratorTypeNVIDIAGPU.String(): {
				AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.String(),
			},
		},
	}
	controller, err := NewStaticNodeClusterController(&StaticNodeClusterControllerOption{
		Store:                      store,
		AcceleratorProfileProvider: profileProvider,
	})
	require.NoError(t, err)

	err = controller.Reconcile(controllerStaticNodeCluster())

	require.NoError(t, err)
	assert.True(t, profileProvider.called)
	assert.Equal(t, []string{v1.AcceleratorTypeNVIDIAGPU.String()}, profileProvider.acceleratorTypes)
	require.Len(t, store.upsertedNodes, 2)
	assert.Equal(t, "head-0", store.upsertedNodes[0].Metadata.Name)
	assert.Equal(t, "worker-0", store.upsertedNodes[1].Metadata.Name)
	assert.Equal(t, "default", store.listWorkspace)
	assert.Equal(t, "static-a", store.listClusterName)
	assert.Equal(t, v1.StaticNodeClusterPhaseProvisioning, store.updatedStatus.Phase)
}

func TestStaticNodeClusterControllerReconcileRejectsWrongType(t *testing.T) {
	controller, err := NewStaticNodeClusterController(&StaticNodeClusterControllerOption{
		Store: &fakeControllerStaticNodeClusterStore{},
	})
	require.NoError(t, err)

	err = controller.Reconcile(&v1.StaticNode{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to assert obj to *v1.StaticNodeCluster")
}

type fakeAcceleratorProfileProvider struct {
	profiles         map[string]*v1.AcceleratorProfile
	called           bool
	acceleratorTypes []string
}

func (f *fakeAcceleratorProfileProvider) GetAcceleratorProfiles(
	_ context.Context,
	acceleratorTypes []string,
) (map[string]*v1.AcceleratorProfile, error) {
	f.called = true
	f.acceleratorTypes = acceleratorTypes

	return f.profiles, nil
}

type fakeControllerStaticNodeClusterStore struct {
	currentNodes    []*v1.StaticNode
	upsertedNodes   []*v1.StaticNode
	deletedNodes    []*v1.StaticNode
	updatedStatus   v1.StaticNodeClusterStatus
	listWorkspace   string
	listClusterName string
}

var _ clusterreconcile.StaticNodeClusterStore = (*fakeControllerStaticNodeClusterStore)(nil)

func (f *fakeControllerStaticNodeClusterStore) ListStaticNodes(
	_ context.Context,
	workspace string,
	clusterName string,
) ([]*v1.StaticNode, error) {
	f.listWorkspace = workspace
	f.listClusterName = clusterName

	return f.currentNodes, nil
}

func (f *fakeControllerStaticNodeClusterStore) UpsertStaticNode(_ context.Context, node *v1.StaticNode) error {
	f.upsertedNodes = append(f.upsertedNodes, node)

	return nil
}

func (f *fakeControllerStaticNodeClusterStore) DeleteStaticNode(_ context.Context, node *v1.StaticNode) error {
	f.deletedNodes = append(f.deletedNodes, node)

	return nil
}

func (f *fakeControllerStaticNodeClusterStore) UpdateStaticNodeClusterStatus(
	_ context.Context,
	_ *v1.StaticNodeCluster,
	status v1.StaticNodeClusterStatus,
) error {
	f.updatedStatus = status

	return nil
}

func controllerStaticNodeCluster() *v1.StaticNodeCluster {
	return &v1.StaticNodeCluster{
		Metadata: &v1.Metadata{
			Workspace: "default",
			Name:      "static-a",
		},
		Spec: &v1.StaticNodeClusterSpec{
			Version:       "v1.2.0",
			ImageRegistry: "registry.example.com/neutree",
			Head: v1.StaticNodeClusterHeadSpec{
				NodeName: "head-0",
			},
			Nodes: []v1.StaticNodeClusterNodeSpec{
				{
					Name:            "head-0",
					IP:              "10.0.0.10",
					AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.String(),
				},
				{
					Name: "worker-0",
					IP:   "10.0.0.11",
				},
			},
		},
	}
}
