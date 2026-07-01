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
			name:     "nil status (not initialized) -> Initializing",
			cluster:  &v1.Cluster{Spec: specV1},
			expected: v1.ClusterPhaseInitializing,
		},
		{
			name:     "initialized=false -> Initializing",
			cluster:  &v1.Cluster{Spec: specV1, Status: &v1.ClusterStatus{Initialized: false}},
			expected: v1.ClusterPhaseInitializing,
		},
		{
			name:     "initialized, empty hash (pre-migration) -> Failed",
			cluster:  &v1.Cluster{Spec: specV1, Status: &v1.ClusterStatus{Initialized: true, ObservedSpecHash: ""}},
			expected: v1.ClusterPhaseFailed,
		},
		{
			name:     "version mismatch -> Upgrading",
			cluster:  &v1.Cluster{Spec: specV2, Status: &v1.ClusterStatus{Initialized: true, Version: "v1.0.0", ObservedSpecHash: hashV1}},
			expected: v1.ClusterPhaseUpgrading,
		},
		{
			name:     "version mismatch takes priority over hash mismatch -> Upgrading",
			cluster:  &v1.Cluster{Spec: specV2, Status: &v1.ClusterStatus{Initialized: true, Version: "v1.0.0", ObservedSpecHash: "stale-hash"}},
			expected: v1.ClusterPhaseUpgrading,
		},
		{
			name:     "same version, hash mismatch -> Updating",
			cluster:  &v1.Cluster{Spec: specV1, Status: &v1.ClusterStatus{Initialized: true, Version: "v1.0.0", ObservedSpecHash: "stale-hash"}},
			expected: v1.ClusterPhaseUpdating,
		},
		{
			name:     "empty status version, hash mismatch -> Updating",
			cluster:  &v1.Cluster{Spec: specV2, Status: &v1.ClusterStatus{Initialized: true, Version: "", ObservedSpecHash: hashV1}},
			expected: v1.ClusterPhaseUpdating,
		},
		{
			name:     "hash mismatch -> Updating",
			cluster:  &v1.Cluster{Spec: specV2, Status: &v1.ClusterStatus{Initialized: true, ObservedSpecHash: hashV1}},
			expected: v1.ClusterPhaseUpdating,
		},
		{
			name:     "initialized, hash matches, not ready -> Failed",
			cluster:  &v1.Cluster{Spec: specV1, Status: &v1.ClusterStatus{Initialized: true, ObservedSpecHash: hashV1}},
			expected: v1.ClusterPhaseFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, DetermineClusterPhase(tt.isResourceReady, tt.cluster))
		})
	}
}

func TestDetermineStaticNodeClusterBackedClusterPhase(t *testing.T) {
	specV1 := &v1.ClusterSpec{Type: v1.SSHClusterType, Version: "v1.0.1", ImageRegistry: "reg"}
	specV2 := &v1.ClusterSpec{Type: v1.SSHClusterType, Version: "v1.0.2", ImageRegistry: "reg"}
	hashV1 := ComputeClusterSpecHash(specV1)

	tests := []struct {
		name            string
		isResourceReady bool
		cluster         *v1.Cluster
		staticStatus    *v1.StaticNodeClusterStatus
		specObserved    bool
		expected        v1.ClusterPhase
	}{
		{
			name:            "resource ready -> Running",
			isResourceReady: true,
			cluster:         &v1.Cluster{Spec: specV1, Status: &v1.ClusterStatus{Initialized: true, ObservedSpecHash: hashV1}},
			staticStatus:    &v1.StaticNodeClusterStatus{Phase: v1.StaticNodeClusterPhaseReady, Version: "v1.0.1"},
			specObserved:    true,
			expected:        v1.ClusterPhaseRunning,
		},
		{
			name:         "not initialized provisioning -> Initializing",
			cluster:      &v1.Cluster{Spec: specV1, Status: &v1.ClusterStatus{}},
			staticStatus: &v1.StaticNodeClusterStatus{Phase: v1.StaticNodeClusterPhaseProvisioning},
			expected:     v1.ClusterPhaseInitializing,
		},
		{
			name:         "initialized provisioning -> Updating",
			cluster:      &v1.Cluster{Spec: specV1, Status: &v1.ClusterStatus{Initialized: true, Version: "v1.0.1", ObservedSpecHash: hashV1}},
			staticStatus: &v1.StaticNodeClusterStatus{Phase: v1.StaticNodeClusterPhaseProvisioning},
			expected:     v1.ClusterPhaseUpdating,
		},
		{
			name:         "static upgrade phase -> Upgrading",
			cluster:      &v1.Cluster{Spec: specV2, Status: &v1.ClusterStatus{Initialized: true, Version: "v1.0.1", ObservedSpecHash: hashV1}},
			staticStatus: &v1.StaticNodeClusterStatus{Phase: v1.StaticNodeClusterPhaseUpgrading, Version: "v1.0.1"},
			expected:     v1.ClusterPhaseUpgrading,
		},
		{
			name:         "static failed phase -> Failed",
			cluster:      &v1.Cluster{Spec: specV1, Status: &v1.ClusterStatus{Initialized: true, Version: "v1.0.1", ObservedSpecHash: hashV1}},
			staticStatus: &v1.StaticNodeClusterStatus{Phase: v1.StaticNodeClusterPhaseFailed},
			expected:     v1.ClusterPhaseFailed,
		},
		{
			name:         "ready but spec not observed -> Updating",
			cluster:      &v1.Cluster{Spec: specV1, Status: &v1.ClusterStatus{Initialized: true, Version: "v1.0.1", ObservedSpecHash: hashV1}},
			staticStatus: &v1.StaticNodeClusterStatus{Phase: v1.StaticNodeClusterPhaseReady, Version: "v1.0.1"},
			expected:     v1.ClusterPhaseUpdating,
		},
		{
			name:         "ready and spec observed but outer reconcile failed -> Updating",
			cluster:      &v1.Cluster{Spec: specV1, Status: &v1.ClusterStatus{Initialized: true, Version: "v1.0.1", ObservedSpecHash: hashV1}},
			staticStatus: &v1.StaticNodeClusterStatus{Phase: v1.StaticNodeClusterPhaseReady, Version: "v1.0.1"},
			specObserved: true,
			expected:     v1.ClusterPhaseUpdating,
		},
		{
			name:         "missing static status and observed spec falls back to default Failed",
			cluster:      &v1.Cluster{Spec: specV1, Status: &v1.ClusterStatus{Initialized: true, Version: "v1.0.1", ObservedSpecHash: hashV1}},
			specObserved: true,
			expected:     v1.ClusterPhaseFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, DetermineStaticNodeClusterBackedClusterPhase(
				tt.isResourceReady,
				tt.cluster,
				tt.staticStatus,
				tt.specObserved,
			))
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
			name:     "force-delete annotation -> Deleted",
			cluster:  &v1.Cluster{Metadata: &v1.Metadata{Annotations: map[string]string{"neutree.ai/force-delete": "true"}}, Spec: &v1.ClusterSpec{}},
			expected: v1.ClusterPhaseDeleted,
		},
		{
			name:     "not completed, no force -> Deleting",
			cluster:  &v1.Cluster{Metadata: &v1.Metadata{}, Spec: &v1.ClusterSpec{}},
			expected: v1.ClusterPhaseDeleting,
		},
		{
			name:     "nil metadata, not completed -> Deleting",
			cluster:  &v1.Cluster{Spec: &v1.ClusterSpec{}},
			expected: v1.ClusterPhaseDeleting,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, DetermineClusterDeletePhase(tt.isDeleteCompleted, tt.cluster))
		})
	}
}
