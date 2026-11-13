package neutree

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// helm uninstall keeps every volume. The claims of StatefulSet
// volumeClaimTemplates are not release resources, so helm never deletes them;
// they only must not ask Kubernetes to delete them with the StatefulSet.
// PostgreSQL's volumes belong to the CloudNativePG Cluster (see
// TestDBUninstallKeepsCluster).
func TestPersistentVolumeClaimsKeepOnUninstall(t *testing.T) {
	for _, tc := range []struct {
		name      string
		values    map[string]interface{}
		pvcs      []string
		templates []string
	}{
		{
			name:      "single",
			values:    nil,
			pvcs:      []string{"neutree-grafana", "neutree-vector-pvc", "neutree-victorialogs-pvc", "neutree-vmagent-pvc"},
			templates: []string{"neutree-victoria-metrics-cluster-vmstorage"},
		},
		{
			name:      "ha",
			values:    haValues(nil),
			pvcs:      []string{"neutree-victorialogs-pvc"},
			templates: []string{"neutree-vector", "neutree-victoria-metrics-cluster-vmstorage"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resources := renderChart(t, tc.values)

			policies := map[string]string{}

			var templates []string

			for _, resource := range resources.Items {
				switch resource.GetKind() {
				case "PersistentVolumeClaim":
					policies[resource.GetName()] = resource.GetAnnotations()["helm.sh/resource-policy"]
				case "StatefulSet":
					claims, _, err := unstructured.NestedSlice(resource.Object, "spec", "volumeClaimTemplates")
					require.NoError(t, err)

					if len(claims) == 0 {
						continue
					}

					templates = append(templates, resource.GetName())
					whenDeleted, _, err := unstructured.NestedString(resource.Object,
						"spec", "persistentVolumeClaimRetentionPolicy", "whenDeleted")
					require.NoError(t, err)
					require.NotEqual(t, "Delete", whenDeleted, resource.GetName())
				}
			}

			expected := map[string]string{}
			for _, name := range tc.pvcs {
				expected[name] = "keep"
			}

			require.Equal(t, expected, policies)
			require.ElementsMatch(t, tc.templates, templates)
		})
	}
}
