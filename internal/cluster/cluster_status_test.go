package cluster

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/assert"
)

func TestDetermineClusterPhase(t *testing.T) {
	specV1 := &v1.ClusterSpec{Type: "ssh", Version: "v1.0.0", ImageRegistry: "reg"}
	specV2 := &v1.ClusterSpec{Type: "ssh", Version: "v2.0.0", ImageRegistry: "reg"}
	hashV1 := ComputeClusterSpecHash(specV1)

	tests := []struct {
		name            string
		isResourceReady bool
		cluster         *v1.Cluster
		expected        v1.ClusterPhase
	}{
		{
			name:            "resource ready -> Running",
			isResourceReady: true,
			cluster:         &v1.Cluster{Spec: specV1, Status: &v1.ClusterStatus{Initialized: true, ObservedSpecHash: hashV1}},
			expected:        v1.ClusterPhaseRunning,
		},
		{
			name:    "nil status (not initialized) -> Initializing",
			cluster: &v1.Cluster{Spec: specV1},
			expected: v1.ClusterPhaseInitializing,
		},
		{
			name:    "initialized=false -> Initializing",
			cluster: &v1.Cluster{Spec: specV1, Status: &v1.ClusterStatus{Initialized: false}},
			expected: v1.ClusterPhaseInitializing,
		},
		{
			name:    "initialized, empty hash (pre-migration) -> Failed",
			cluster: &v1.Cluster{Spec: specV1, Status: &v1.ClusterStatus{Initialized: true, ObservedSpecHash: ""}},
			expected: v1.ClusterPhaseFailed,
		},
		{
			name:    "hash mismatch -> Updating",
			cluster: &v1.Cluster{Spec: specV2, Status: &v1.ClusterStatus{Initialized: true, ObservedSpecHash: hashV1}},
			expected: v1.ClusterPhaseUpdating,
		},
		{
			name:    "initialized, hash matches, not ready -> Failed",
			cluster: &v1.Cluster{Spec: specV1, Status: &v1.ClusterStatus{Initialized: true, ObservedSpecHash: hashV1}},
			expected: v1.ClusterPhaseFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, DetermineClusterPhase(tt.isResourceReady, tt.cluster))
		})
	}
}

func TestDetermineClusterDeletePhase(t *testing.T) {
	tests := []struct {
		name              string
		isDeleteCompleted bool
		cluster           *v1.Cluster
		expected          v1.ClusterPhase
	}{
		{
			name:              "delete completed -> Deleted",
			isDeleteCompleted: true,
			cluster:           &v1.Cluster{Spec: &v1.ClusterSpec{}},
			expected:          v1.ClusterPhaseDeleted,
		},
		{
			name:    "force-delete annotation -> Deleted",
			cluster: &v1.Cluster{Metadata: &v1.Metadata{Annotations: map[string]string{"neutree.ai/force-delete": "true"}}, Spec: &v1.ClusterSpec{}},
			expected: v1.ClusterPhaseDeleted,
		},
		{
			name:    "not completed, no force -> Deleting",
			cluster: &v1.Cluster{Metadata: &v1.Metadata{}, Spec: &v1.ClusterSpec{}},
			expected: v1.ClusterPhaseDeleting,
		},
		{
			name:    "nil metadata, not completed -> Deleting",
			cluster: &v1.Cluster{Spec: &v1.ClusterSpec{}},
			expected: v1.ClusterPhaseDeleting,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, DetermineClusterDeletePhase(tt.isDeleteCompleted, tt.cluster))
		})
	}
}
