package util

import (
	"testing"

	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
)

func TestKubernetesClientSchemeRegistersRBAC(t *testing.T) {
	kinds, _, err := scheme.ObjectKinds(&rbacv1.Role{})

	require.NoError(t, err)
	require.NotEmpty(t, kinds)
	require.Equal(t, "Role", kinds[0].Kind)
}
