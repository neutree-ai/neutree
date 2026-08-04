package cluster

import (
	"errors"
	"testing"

	"github.com/neutree-ai/neutree/pkg/storage"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestNewReconcileDispatchesStaticNodeBackedSSHClusterByVersion(t *testing.T) {
	tests := []struct {
		name       string
		version    string
		wantStatic bool
	}{
		{
			name:    "SSH v1.0.1 uses legacy Ray SSH reconciler",
			version: "v1.0.1",
		},
		{
			name:       "SSH v1.0.2 uses static Ray reconciler",
			version:    "v1.0.2",
			wantStatic: true,
		},
		{
			name:       "SSH v1.1.0 uses static Ray reconciler",
			version:    "v1.1.0",
			wantStatic: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler, err := NewReconcile(&v1.Cluster{
				Spec: &v1.ClusterSpec{Type: v1.SSHClusterType, Version: tt.version},
			}, nil, nil, "")

			require.NoError(t, err)
			_, isStatic := reconciler.(*staticRayReconciler)
			assert.Equal(t, tt.wantStatic, isStatic)
		})
	}
}

func TestNewReconcileRejectsInvalidClusterVersion(t *testing.T) {
	reconciler, err := NewReconcile(&v1.Cluster{
		Spec: &v1.ClusterSpec{Type: v1.SSHClusterType, Version: "custom"},
	}, nil, nil, "")

	require.Error(t, err)
	assert.Nil(t, reconciler)
	assert.Contains(t, err.Error(), "invalid cluster version")
}

func TestNewReconcileWithReleaseInfoResolvesReleaseAwareClusterComponents(t *testing.T) {
	resolver := componentResolverFunc(func(version, acceleratorType string) (map[string]string, error) {
		assert.Equal(t, "v1.1.0", version)
		assert.Equal(t, "amd_gpu", acceleratorType)
		return map[string]string{"ray_runtime": "neutree/neutree-serve:v1.1.0-rocm", "router": "neutree/router:v1.1.0"}, nil
	})

	reconciler, err := NewReconcileWithReleaseInfo(&v1.Cluster{
		Spec: &v1.ClusterSpec{
			Type:    v1.SSHClusterType,
			Version: "v1.1.0",
			Config:  &v1.ClusterConfig{AcceleratorType: stringPointer("amd_gpu")},
		},
	}, nil, nil, "", resolver)

	require.NoError(t, err)
	staticReconciler, ok := reconciler.(*staticRayReconciler)
	require.True(t, ok)
	assert.Equal(t, "neutree/neutree-serve:v1.1.0-rocm", staticReconciler.releaseComponents["ray_runtime"])
}

func TestNewReconcileWithReleaseInfoRejectsMissingReleaseMatrix(t *testing.T) {
	resolver := componentResolverFunc(func(string, string) (map[string]string, error) {
		return nil, errors.New("release info v1.2.0 not found")
	})

	reconciler, err := NewReconcileWithReleaseInfo(&v1.Cluster{
		Spec: &v1.ClusterSpec{Type: v1.SSHClusterType, Version: "v1.1.0"},
	}, nil, nil, "", resolver)

	require.ErrorContains(t, err, "resolve release components")
	assert.Nil(t, reconciler)
}

func TestNewReconcileWithReleaseInfoPersistsUpgradeSnapshot(t *testing.T) {
	store := new(storagemocks.MockStorage)
	resolver := &releaseInfoResolver{
		info: &v1.ReleaseInfo{
			Metadata: &v1.Metadata{Name: "v1.2.0"},
			Spec: &v1.ReleaseInfoSpec{ClusterVersions: []v1.ReleaseInfoClusterVersion{
				{
					Version:   "v1.1.0",
					State:     v1.ReleaseInfoClusterVersionStateActive,
					UpgradeTo: []string{"v1.2.0"},
				},
				{
					Version:    "v1.2.0",
					State:      v1.ReleaseInfoClusterVersionStateActive,
					Components: map[string]string{"ray_runtime": "neutree/neutree-serve:v1.1.1"},
				},
			}},
			Status: &v1.ReleaseInfoStatus{Revision: "revision-2"},
		},
		components: map[string]string{"ray_runtime": "neutree/neutree-serve:v1.1.1"},
	}
	cluster := &v1.Cluster{
		ID: 7,
		Spec: &v1.ClusterSpec{
			Type:    v1.SSHClusterType,
			Version: "v1.2.0",
		},
		Status: &v1.ClusterStatus{Version: "v1.1.0"},
	}

	store.On("GetClusterUpgradeSnapshot", "7").Return(nil, storage.ErrResourceNotFound).Once()
	store.On("CreateClusterUpgradeSnapshot", mock.MatchedBy(func(snapshot *v1.ClusterUpgradeSnapshot) bool {
		return snapshot.ClusterID == 7 &&
			snapshot.SourceClusterVersion == "v1.1.0" &&
			snapshot.TargetClusterVersion == "v1.2.0" &&
			snapshot.SourceReleaseInfo.Baseline == "v1.2.0" &&
			snapshot.TargetReleaseInfo.Baseline == "v1.2.0" &&
			snapshot.TargetReleaseInfo.Revision == "revision-2" &&
			snapshot.Components["ray_runtime"] == "neutree/neutree-serve:v1.1.1"
	})).Return(nil).Once()

	reconciler, err := NewReconcileWithReleaseInfo(cluster, nil, store, "", resolver)

	require.NoError(t, err)
	staticReconciler, ok := reconciler.(*staticRayReconciler)
	require.True(t, ok)
	assert.Equal(t, "neutree/neutree-serve:v1.1.1", staticReconciler.releaseComponents["ray_runtime"])
	store.AssertExpectations(t)
}

func TestNewReconcileWithReleaseInfoSnapshotsComponentsFromCurrentReleaseInfo(t *testing.T) {
	store := new(storagemocks.MockStorage)
	resolver := &releaseInfoResolver{
		info: &v1.ReleaseInfo{
			Metadata: &v1.Metadata{Name: "v1.2.0"},
			Spec: &v1.ReleaseInfoSpec{ClusterVersions: []v1.ReleaseInfoClusterVersion{
				{
					Version:   "v1.1.0",
					State:     v1.ReleaseInfoClusterVersionStateActive,
					UpgradeTo: []string{"v1.2.0"},
				},
				{
					Version: "v1.2.0",
					State:   v1.ReleaseInfoClusterVersionStateActive,
					Components: map[string]string{
						"ray_runtime": "registry.example/release-info-runtime:v1.2.0",
					},
					AcceleratorComponents: map[string]map[string]string{
						"amd_gpu": {"ray_runtime": "registry.example/release-info-runtime:v1.2.0-rocm"},
					},
				},
			}},
			Status: &v1.ReleaseInfoStatus{Revision: "nightly-revision-9"},
		},
		components: map[string]string{"ray_runtime": "registry.example/stale-components-for:v0"},
	}
	acceleratorType := "amd_gpu"
	cluster := &v1.Cluster{
		ID: 8,
		Spec: &v1.ClusterSpec{
			Type:    v1.SSHClusterType,
			Version: "v1.2.0",
			Config:  &v1.ClusterConfig{AcceleratorType: &acceleratorType},
		},
		Status: &v1.ClusterStatus{Version: "v1.1.0"},
	}

	store.On("GetClusterUpgradeSnapshot", "8").Return(nil, storage.ErrResourceNotFound).Once()
	store.On("CreateClusterUpgradeSnapshot", mock.MatchedBy(func(snapshot *v1.ClusterUpgradeSnapshot) bool {
		return snapshot.TargetReleaseInfo.Revision == "nightly-revision-9" &&
			snapshot.Components["ray_runtime"] == "registry.example/release-info-runtime:v1.2.0-rocm"
	})).Return(nil).Once()

	reconciler, err := NewReconcileWithReleaseInfo(cluster, nil, store, "", resolver)

	require.NoError(t, err)
	staticReconciler, ok := reconciler.(*staticRayReconciler)
	require.True(t, ok)
	assert.Equal(t, "registry.example/release-info-runtime:v1.2.0-rocm", staticReconciler.releaseComponents["ray_runtime"])
	store.AssertExpectations(t)
}

func TestNewReconcileWithReleaseInfoReusesUpgradeSnapshot(t *testing.T) {
	store := new(storagemocks.MockStorage)
	cluster := &v1.Cluster{
		ID: 7,
		Spec: &v1.ClusterSpec{
			Type:    v1.SSHClusterType,
			Version: "v1.2.0",
		},
		Status: &v1.ClusterStatus{Version: "v1.1.0"},
	}
	store.On("GetClusterUpgradeSnapshot", "7").Return(&v1.ClusterUpgradeSnapshot{
		ClusterID:            7,
		SourceClusterVersion: "v1.1.0",
		TargetClusterVersion: "v1.2.0",
		Components:           map[string]string{"ray_runtime": "snapshot/serve:v1.1.1"},
	}, nil).Once()

	reconciler, err := NewReconcileWithReleaseInfo(cluster, nil, store, "", componentResolverFunc(func(string, string) (map[string]string, error) {
		t.Fatal("resolver must not be called when an upgrade snapshot exists")
		return nil, nil
	}))

	require.NoError(t, err)
	staticReconciler, ok := reconciler.(*staticRayReconciler)
	require.True(t, ok)
	assert.Equal(t, "snapshot/serve:v1.1.1", staticReconciler.releaseComponents["ray_runtime"])
	store.AssertExpectations(t)
}

type componentResolverFunc func(string, string) (map[string]string, error)

func (resolver componentResolverFunc) ComponentsFor(version, acceleratorType string) (map[string]string, error) {
	return resolver(version, acceleratorType)
}

type releaseInfoResolver struct {
	info       *v1.ReleaseInfo
	components map[string]string
}

func (resolver *releaseInfoResolver) ComponentsFor(string, string) (map[string]string, error) {
	return resolver.components, nil
}

func (resolver *releaseInfoResolver) Current() (*v1.ReleaseInfo, error) {
	return resolver.info, nil
}

func stringPointer(value string) *string {
	return &value
}
