package accelerator

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNvidiaOperatorManagementRequiresBothFlags(t *testing.T) {
	for _, tc := range []struct {
		name        string
		spec        map[string]interface{}
		wantManaged bool
	}{
		{name: "explicit dual management", spec: map[string]interface{}{"driver": map[string]interface{}{"enabled": true}, "toolkit": map[string]interface{}{"enabled": true}}, wantManaged: true},
		{name: "driver only", spec: map[string]interface{}{"driver": map[string]interface{}{"enabled": true}, "toolkit": map[string]interface{}{"enabled": false}}},
		{name: "toolkit only", spec: map[string]interface{}{"driver": map[string]interface{}{"enabled": false}, "toolkit": map[string]interface{}{"enabled": true}}},
		{name: "missing management flag", spec: map[string]interface{}{"driver": map[string]interface{}{"enabled": true}}},
		{name: "empty policy", spec: map[string]interface{}{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			managed, _, err := NvidiaOperatorRuntime([]GPUOperatorClusterPolicy{{Name: "policy", Spec: tc.spec}})
			require.NoError(t, err)
			require.Equal(t, tc.wantManaged, managed)
		})
	}
}
