package neutree

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPersistentVolumeClaimsKeepOnUninstall(t *testing.T) {
	resources := renderChart(t, nil)

	expectedPVCs := []string{
		"neutree-grafana",
		"neutree-postgresql-pvc",
		"neutree-vector-pvc",
		"neutree-victorialogs-pvc",
		"neutree-vmagent-pvc",
	}

	actualPolicies := map[string]string{}
	for _, resource := range resources.Items {
		if resource.GetKind() != "PersistentVolumeClaim" {
			continue
		}
		annotations := resource.GetAnnotations()
		actualPolicies[resource.GetName()] = annotations["helm.sh/resource-policy"]
	}

	expectedPolicies := map[string]string{}
	for _, name := range expectedPVCs {
		expectedPolicies[name] = "keep"
	}
	require.Equal(t, expectedPolicies, actualPolicies)
}
