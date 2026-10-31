package neutree

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Components whose values block carries nodeSelector / affinity / tolerations.
var schedulingComponents = []string{
	"db", "auth", "postgrest", "postgresmeta", "api", "core",
	"kong", "vector", "victorialogs", "vmagent", "migration",
}

var (
	testNodeSelector = map[string]interface{}{"neutree.test/pool": "control-plane"}
	testToleration   = map[string]interface{}{
		"key": "neutree.test/dedicated", "operator": "Exists", "effect": "NoSchedule",
	}
	testAffinity = map[string]interface{}{
		"nodeAffinity": map[string]interface{}{
			"requiredDuringSchedulingIgnoredDuringExecution": map[string]interface{}{
				"nodeSelectorTerms": []interface{}{
					map[string]interface{}{
						"matchExpressions": []interface{}{
							map[string]interface{}{
								"key": "neutree.test/pool", "operator": "In", "values": []interface{}{"control-plane"},
							},
						},
					},
				},
			},
		},
	}
)

func schedulingOverrides() map[string]interface{} {
	overrides := map[string]interface{}{}
	for _, c := range schedulingComponents {
		overrides[c] = map[string]interface{}{
			"nodeSelector": testNodeSelector,
			"tolerations":  []interface{}{testToleration},
			"affinity":     testAffinity,
		}
	}

	// Render the optional workloads too.
	overrides["victorialogs"].(map[string]interface{})["enabled"] = true

	return overrides
}

// parentWorkloads returns pod-template workloads rendered by this chart's own
// templates; sub-charts (grafana, victoria-metrics-cluster) have their own values.
func parentWorkloads(resources *unstructured.UnstructuredList) []unstructured.Unstructured {
	var out []unstructured.Unstructured

	for _, r := range resources.Items {
		switch r.GetKind() {
		case "Deployment", "StatefulSet", "DaemonSet", "Job":
		default:
			continue
		}

		if strings.Contains(r.GetName(), "grafana") || strings.Contains(r.GetName(), "victoria-metrics-cluster") {
			continue
		}

		out = append(out, r)
	}

	return out
}

func TestAllWorkloadsHonorSchedulingValues(t *testing.T) {
	resources := renderChart(t, schedulingOverrides())

	workloads := parentWorkloads(resources)
	require.NotEmpty(t, workloads)

	seen := map[string]bool{}

	for _, w := range workloads {
		name := w.GetKind() + "/" + w.GetName()
		seen[name] = true

		nodeSelector, found, err := unstructured.NestedStringMap(w.Object, "spec", "template", "spec", "nodeSelector")
		require.NoError(t, err, name)
		require.True(t, found, "%s has no nodeSelector", name)
		require.Equal(t, "control-plane", nodeSelector["neutree.test/pool"], name)

		tolerations, found, err := unstructured.NestedSlice(w.Object, "spec", "template", "spec", "tolerations")
		require.NoError(t, err, name)
		require.True(t, found, "%s has no tolerations", name)
		require.Equal(t, []interface{}{testToleration}, tolerations, name)

		_, found, err = unstructured.NestedMap(w.Object, "spec", "template", "spec", "affinity", "nodeAffinity")
		require.NoError(t, err, name)
		require.True(t, found, "%s has no affinity", name)
	}

	// Guard against a workload silently dropping out of the check.
	for _, want := range []string{
		"Deployment/neutree-kong", "Deployment/neutree-vector", "Deployment/neutree-vmagent",
		"Job/neutree-migration-job-1", "Job/neutree-post-migration-hook-job-1",
		"Job/neutree-kong-init-migrations-1",
	} {
		require.True(t, seen[want], "workload %s was not rendered; seen %v", want, seen)
	}
}

func TestDefaultValuesRenderNoSchedulingFields(t *testing.T) {
	resources := renderChart(t, nil)

	for _, w := range parentWorkloads(resources) {
		for _, field := range []string{"nodeSelector", "tolerations", "affinity"} {
			_, found, err := unstructured.NestedFieldNoCopy(w.Object, "spec", "template", "spec", field)
			require.NoError(t, err)
			require.False(t, found, "%s/%s renders %s with default values", w.GetKind(), w.GetName(), field)
		}
	}
}
