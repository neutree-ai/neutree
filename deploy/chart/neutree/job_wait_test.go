package neutree

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"helm.sh/helm/v3/pkg/chartutil"
	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestJobWaitersAllowRetriedPods(t *testing.T) {
	cases := []struct {
		name     string
		revision int
	}{
		{name: "install", revision: 1},
		{name: "upgrade", revision: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resources, err := renderChartForRelease(nil, chartutil.ReleaseOptions{
				Name:      "neutree",
				Namespace: "job-retry-test",
				Revision:  tc.revision,
				IsInstall: tc.name == "install",
				IsUpgrade: tc.name == "upgrade",
			})
			require.NoError(t, err)

			expected := map[string][]string{}
			for _, dependency := range []struct{ job, waiter, target string }{
				{"post-migration-hook-job", "wait-migration-job", "migration-job"},
				{"kong-post-upgrade-migrations", "wait-migration-job", "migration-job"},
				{"kong-post-upgrade-migrations", "wait-kong-pre-migrate-job", "kong-pre-upgrade-migrations"},
				{"kong-pre-upgrade-migrations", "wait-migration-job", "migration-job"},
				{"kong-pre-upgrade-migrations", "wait-kong-bootstrap-job", "kong-init-migrations"},
				{"kong-init-migrations", "wait-migration-job", "migration-job"},
			} {
				key := fmt.Sprintf("neutree-%s-%d/%s", dependency.job, tc.revision, dependency.waiter)
				expected[key] = []string{
					"job-wr", fmt.Sprintf("neutree-%s-%d", dependency.target, tc.revision), "-n", "job-retry-test",
				}
			}

			actual := map[string][]string{}
			for _, resource := range resources.Items {
				if resource.GetKind() != "Job" {
					continue
				}
				var job batchv1.Job
				require.NoError(t, runtime.DefaultUnstructuredConverter.FromUnstructured(resource.Object, &job))
				for _, container := range job.Spec.Template.Spec.InitContainers {
					if len(container.Args) > 0 && (container.Args[0] == "job" || container.Args[0] == "job-wr") {
						actual[job.Name+"/"+container.Name] = container.Args
					}
				}
			}
			require.Equal(t, expected, actual)
		})
	}
}
