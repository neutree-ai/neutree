package cluster

import (
	"testing"

	"github.com/stretchr/testify/assert"
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

func TestNewReconcileWithClusterProfileUsesExactVersionForKubernetes(t *testing.T) {
	tests := []struct {
		name    string
		version string
		tag     string
	}{
		{name: "stable", version: "v1.2.0", tag: "v1.1.1"},
		{name: "alpha", version: "v1.2.0-alpha.1", tag: "v1.1.1-alpha.1"},
		{name: "release candidate", version: "v1.2.0-rc.1", tag: "v1.1.1-rc.1"},
		{name: "v1.1 alpha", version: "v1.1.0-alpha.1", tag: "v1.1.0-alpha.1"},
		{name: "v1.1 release candidate", version: "v1.1.0-rc.1", tag: "v1.1.0-rc.1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolvedVersion := ""
			resolver := clusterProfileComponentResolverFunc(func(version string) (v1.ClusterProfileComponents, error) {
				resolvedVersion = version
				return v1.ClusterProfileComponents{
					RayRuntime: v1.ImageRef{Image: "neutree/neutree-serve", Tag: tt.tag},
					Router:     v1.ImageRef{Image: "neutree/router", Tag: tt.tag},
				}, nil
			})

			reconciler, err := NewReconcileWithClusterProfile(&v1.Cluster{
				Spec: &v1.ClusterSpec{Type: v1.KubernetesClusterType, Version: tt.version},
			}, nil, nil, "", resolver)

			require.NoError(t, err)
			assert.Equal(t, tt.version, resolvedVersion)
			nativeReconciler, ok := reconciler.(*NativeKubernetesClusterReconciler)
			require.True(t, ok)
			assert.Equal(t, tt.tag, nativeReconciler.profileComponents.RayRuntime.Tag)
			assert.Equal(t, tt.tag, nativeReconciler.profileComponents.Router.Tag)
		})
	}
}

func TestNewReconcileWithClusterProfileUsesTargetProfileDuringUpgradeWithoutSnapshot(t *testing.T) {
	resolver := clusterProfileComponentResolverFunc(func(version string) (v1.ClusterProfileComponents, error) {
		assert.Equal(t, "v1.2.0", version)

		return v1.ClusterProfileComponents{
			RayRuntime: v1.ImageRef{Image: "neutree/neutree-serve", Tag: "v1.1.1"},
		}, nil
	})
	cluster := &v1.Cluster{
		ID: 7,
		Spec: &v1.ClusterSpec{
			Type:    v1.SSHClusterType,
			Version: "v1.2.0",
		},
		Status: &v1.ClusterStatus{Version: "v1.1.0"},
	}

	reconciler, err := NewReconcileWithClusterProfile(cluster, nil, nil, "", resolver)

	require.NoError(t, err)
	staticReconciler, ok := reconciler.(*staticRayReconciler)
	require.True(t, ok)
	assert.Equal(t, "v1.1.1", staticReconciler.profileComponents.RayRuntime.Tag)
}

type clusterProfileComponentResolverFunc func(string) (v1.ClusterProfileComponents, error)

func (resolver clusterProfileComponentResolverFunc) ComponentsFor(version string) (v1.ClusterProfileComponents, error) {
	return resolver(version)
}

func stringPointer(value string) *string {
	return &value
}
