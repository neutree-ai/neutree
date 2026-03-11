package cluster

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestWriteEarlyStatus_Initializing(t *testing.T) {
	s := storagemocks.NewMockStorage(t)
	cluster := &v1.Cluster{
		ID:       1,
		Metadata: &v1.Metadata{Workspace: "ws", Name: "c1"},
		Spec:     &v1.ClusterSpec{Version: "v1"},
		// nil status -> not initialized -> Initializing
	}

	s.EXPECT().UpdateCluster("1", mock.MatchedBy(func(c *v1.Cluster) bool {
		return c.Status != nil && c.Status.Phase == v1.ClusterPhaseInitializing
	})).Return(nil)

	WriteEarlyStatus(cluster, s)
	assert.Equal(t, v1.ClusterPhaseInitializing, cluster.Status.Phase)
}

func TestWriteEarlyStatus_Updating(t *testing.T) {
	spec := &v1.ClusterSpec{
		Type:          "ssh",
		Version:       "v2.0.0",
		ImageRegistry: "reg",
	}
	oldHash := ComputeClusterSpecHash(&v1.ClusterSpec{
		Type:          "ssh",
		Version:       "v1.0.0",
		ImageRegistry: "reg",
	})

	s := storagemocks.NewMockStorage(t)
	cluster := &v1.Cluster{
		ID:       2,
		Metadata: &v1.Metadata{Workspace: "ws", Name: "c2"},
		Spec:     spec,
		Status: &v1.ClusterStatus{
			Initialized:      true,
			ObservedSpecHash: oldHash,
			Phase:            v1.ClusterPhaseRunning,
		},
	}

	s.EXPECT().UpdateCluster("2", mock.MatchedBy(func(c *v1.Cluster) bool {
		return c.Status != nil && c.Status.Phase == v1.ClusterPhaseUpdating
	})).Return(nil)

	WriteEarlyStatus(cluster, s)
	assert.Equal(t, v1.ClusterPhaseUpdating, cluster.Status.Phase)
}

func TestWriteEarlyStatus_SkipsWhenPhaseAlreadyMatches(t *testing.T) {
	s := storagemocks.NewMockStorage(t)
	cluster := &v1.Cluster{
		ID:       3,
		Metadata: &v1.Metadata{Workspace: "ws", Name: "c3"},
		Spec:     &v1.ClusterSpec{Version: "v1"},
		Status:   &v1.ClusterStatus{Phase: v1.ClusterPhaseInitializing},
		// not initialized + phase already Initializing -> skip
	}

	// No UpdateCluster call expected
	WriteEarlyStatus(cluster, s)
}

func TestWriteEarlyStatus_SkipsForFailedPhase(t *testing.T) {
	spec := &v1.ClusterSpec{
		Type:          "ssh",
		Version:       "v1.0.0",
		ImageRegistry: "reg",
	}
	currentHash := ComputeClusterSpecHash(spec)

	s := storagemocks.NewMockStorage(t)
	cluster := &v1.Cluster{
		ID:       4,
		Metadata: &v1.Metadata{Workspace: "ws", Name: "c4"},
		Spec:     spec,
		Status: &v1.ClusterStatus{
			Initialized:      true,
			ObservedSpecHash: currentHash,
			Phase:            v1.ClusterPhaseRunning,
		},
	}

	// DetermineClusterPhase returns Failed (initialized, hash matches, not ready)
	// WriteEarlyStatus should skip since phase is neither Initializing nor Updating
	WriteEarlyStatus(cluster, s)
}

func TestWriteEarlyStatus_Upgrading(t *testing.T) {
	s := storagemocks.NewMockStorage(t)
	cluster := &v1.Cluster{
		ID:       10,
		Metadata: &v1.Metadata{Workspace: "ws", Name: "c10"},
		Spec:     &v1.ClusterSpec{Type: "ssh", Version: "v2.0.0", ImageRegistry: "reg"},
		Status: &v1.ClusterStatus{
			Initialized:      true,
			Version:          "v1.0.0",
			ObservedSpecHash: "old-hash",
			Phase:            v1.ClusterPhaseRunning,
		},
	}

	s.EXPECT().UpdateCluster("10", mock.MatchedBy(func(c *v1.Cluster) bool {
		return c.Status != nil && c.Status.Phase == v1.ClusterPhaseUpgrading
	})).Return(nil)

	WriteEarlyStatus(cluster, s)
	assert.Equal(t, v1.ClusterPhaseUpgrading, cluster.Status.Phase)
}

func TestWriteEarlyStatus_SkipsWhenPhaseAlreadyUpgrading(t *testing.T) {
	s := storagemocks.NewMockStorage(t)
	cluster := &v1.Cluster{
		ID:       11,
		Metadata: &v1.Metadata{Workspace: "ws", Name: "c11"},
		Spec:     &v1.ClusterSpec{Type: "ssh", Version: "v2.0.0", ImageRegistry: "reg"},
		Status: &v1.ClusterStatus{
			Initialized:      true,
			Version:          "v1.0.0",
			ObservedSpecHash: "old-hash",
			Phase:            v1.ClusterPhaseUpgrading,
		},
	}

	// No UpdateCluster call expected since phase already matches
	WriteEarlyStatus(cluster, s)
}

func TestWriteEarlyDeleting_WritesPhase(t *testing.T) {
	s := storagemocks.NewMockStorage(t)
	cluster := &v1.Cluster{
		ID:       5,
		Metadata: &v1.Metadata{Workspace: "ws", Name: "c5"},
		Status:   &v1.ClusterStatus{Phase: v1.ClusterPhaseRunning},
	}

	s.EXPECT().UpdateCluster("5", mock.MatchedBy(func(c *v1.Cluster) bool {
		return c.Status != nil && c.Status.Phase == v1.ClusterPhaseDeleting
	})).Return(nil)

	WriteEarlyDeleting(cluster, s)
	assert.Equal(t, v1.ClusterPhaseDeleting, cluster.Status.Phase)
}

func TestWriteEarlyDeleting_SkipsWhenAlreadyDeleting(t *testing.T) {
	s := storagemocks.NewMockStorage(t)
	cluster := &v1.Cluster{
		ID:       6,
		Metadata: &v1.Metadata{Workspace: "ws", Name: "c6"},
		Status:   &v1.ClusterStatus{Phase: v1.ClusterPhaseDeleting},
	}

	// No UpdateCluster call expected
	WriteEarlyDeleting(cluster, s)
}

func TestWriteEarlyDeleting_WritesWhenNilStatus(t *testing.T) {
	s := storagemocks.NewMockStorage(t)
	cluster := &v1.Cluster{
		ID:       7,
		Metadata: &v1.Metadata{Workspace: "ws", Name: "c7"},
	}

	s.EXPECT().UpdateCluster("7", mock.MatchedBy(func(c *v1.Cluster) bool {
		return c.Status != nil && c.Status.Phase == v1.ClusterPhaseDeleting
	})).Return(nil)

	WriteEarlyDeleting(cluster, s)
	assert.Equal(t, v1.ClusterPhaseDeleting, cluster.Status.Phase)
}
