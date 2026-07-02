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
