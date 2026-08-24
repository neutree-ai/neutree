package cluster

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
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
			reconciler, err := NewReconcileWithClusterProfile(&v1.Cluster{
				Spec: &v1.ClusterSpec{Type: v1.SSHClusterType, Version: tt.version},
			}, nil, nil, "", testClusterProfileComponentResolver())

			require.NoError(t, err)
			_, isStatic := reconciler.(*staticRayReconciler)
			assert.Equal(t, tt.wantStatic, isStatic)
		})
	}
}

func TestNewReconcileRejectsInvalidClusterVersion(t *testing.T) {
	reconciler, err := NewReconcile(&v1.Cluster{
		Spec: &v1.ClusterSpec{Type: v1.SSHClusterType, Version: "custom"},
	}, nil, storagemocks.NewMockStorage(t), "")

	require.Error(t, err)
	assert.Nil(t, reconciler)
	assert.Contains(t, err.Error(), "invalid cluster version")
}

func TestNewReconcileRequiresStorage(t *testing.T) {
	reconciler, err := NewReconcile(&v1.Cluster{
		Spec: &v1.ClusterSpec{Type: v1.SSHClusterType, Version: "v1.0.1"},
	}, nil, nil, "")

	require.EqualError(t, err, "storage is required to resolve cluster profile")
	assert.Nil(t, reconciler)
}

func TestNewDeleteReconcileDoesNotResolveClusterProfile(t *testing.T) {
	tests := []struct {
		name        string
		clusterType string
		wantType    interface{}
	}{
		{
			name:        "SSH delete accepts a legacy version without a profile",
			clusterType: v1.SSHClusterType,
			wantType:    &sshDeleteReconciler{},
		},
		{
			name:        "Kubernetes delete accepts a legacy version without a profile",
			clusterType: v1.KubernetesClusterType,
			wantType:    &NativeKubernetesClusterReconciler{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler, err := NewDeleteReconcile(&v1.Cluster{
				Metadata: &v1.Metadata{Name: "test-cluster", Workspace: "default"},
				Spec: &v1.ClusterSpec{
					Type:    tt.clusterType,
					Version: "legacy-version-without-profile",
				},
			}, nil, nil, "")

			require.NoError(t, err)
			assert.IsType(t, tt.wantType, reconciler)
		})
	}
}

func TestNewReconcileWithClusterProfileUsesExactSSHProfile(t *testing.T) {
	resolvedVersion := ""
	resolver := clusterProfileComponentResolverFunc(func(version string) (v1.ClusterProfileComponents, error) {
		resolvedVersion = version

		return v1.ClusterProfileComponents{
			RayRuntime: v1.ImageRef{Image: "neutree/neutree-serve", Tag: "v1.1.1"},
		}, nil
	})

	reconciler, err := NewReconcileWithClusterProfile(&v1.Cluster{
		Spec: &v1.ClusterSpec{Type: v1.SSHClusterType, Version: "v1.0.1"},
	}, nil, nil, "", resolver)

	require.NoError(t, err)
	assert.Equal(t, "v1.0.1", resolvedVersion)
	sshReconciler, ok := reconciler.(*sshRayClusterReconciler)
	require.True(t, ok)
	assert.True(t, sshReconciler.profileSelected)
	assert.Equal(t, "v1.1.1", sshReconciler.profileComponents.RayRuntime.Tag)
}

func TestNewReconcileWithClusterProfileUsesExactKubernetesProfile(t *testing.T) {
	resolvedVersion := ""
	resolvedType := ""
	resolver := typedClusterProfileComponentResolverFunc(func(version, clusterType string) (v1.ClusterProfileComponents, error) {
		resolvedVersion = version
		resolvedType = clusterType

		return v1.ClusterProfileComponents{
			Router: v1.ImageRef{Image: "neutree/router", Tag: "v1.2.1"},
		}, nil
	})

	reconciler, err := NewReconcileWithClusterProfile(&v1.Cluster{
		Spec: &v1.ClusterSpec{Type: v1.KubernetesClusterType, Version: "v1.2.0"},
	}, nil, nil, "", resolver)

	require.NoError(t, err)
	assert.Equal(t, "v1.2.0", resolvedVersion)
	assert.Equal(t, v1.KubernetesClusterType, resolvedType)
	kubernetesReconciler, ok := reconciler.(*NativeKubernetesClusterReconciler)
	require.True(t, ok)
	assert.True(t, kubernetesReconciler.profileSelected)
	assert.Equal(t, "v1.2.1", kubernetesReconciler.profileComponents.Router.Tag)
}

type clusterProfileComponentResolverFunc func(string) (v1.ClusterProfileComponents, error)

func (resolver clusterProfileComponentResolverFunc) ComponentsFor(version, _ string) (v1.ClusterProfileComponents, error) {
	return resolver(version)
}

type typedClusterProfileComponentResolverFunc func(string, string) (v1.ClusterProfileComponents, error)

func (resolver typedClusterProfileComponentResolverFunc) ComponentsFor(version, clusterType string) (v1.ClusterProfileComponents, error) {
	return resolver(version, clusterType)
}

func testClusterProfileComponentResolver() ClusterProfileComponentResolver {
	return clusterProfileComponentResolverFunc(func(version string) (v1.ClusterProfileComponents, error) {
		return v1.ClusterProfileComponents{
			RayRuntime: v1.ImageRef{Image: "neutree/neutree-serve", Tag: version},
		}, nil
	})
}
