package neutree

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"helm.sh/helm/v3/pkg/chartutil"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

const (
	dbHost          = "neutree-pg-rw"
	keepaliveParams = "keepalives=1&keepalives_idle=10&keepalives_interval=5&keepalives_count=3&tcp_user_timeout=10000"
)

func renderedCluster(t *testing.T, resources *unstructured.UnstructuredList) map[string]interface{} {
	t.Helper()

	for _, r := range resources.Items {
		if r.GetKind() == "Cluster" && r.GetAPIVersion() == "postgresql.cnpg.io/v1" {
			require.Equal(t, "neutree-pg", r.GetName())

			spec, _, err := unstructured.NestedMap(r.Object, "spec")
			require.NoError(t, err)

			return spec
		}
	}

	t.Fatal("no CloudNativePG Cluster rendered")

	return nil
}

func TestDBRequiresCloudNativePG(t *testing.T) {
	release := chartutil.ReleaseOptions{Name: "neutree", Namespace: "default", Revision: 1, IsInstall: true}

	_, err := renderChartWithAPIs(nil, release, false)
	require.ErrorContains(t, err, "postgresql.cnpg.io/v1")
	require.ErrorContains(t, err, "db.operatorCheck=false")

	resources, err := renderChartWithAPIs(map[string]interface{}{
		"db": map[string]interface{}{"operatorCheck": false},
	}, release, false)
	require.NoError(t, err)
	renderedCluster(t, resources)
}

func TestDBClusterSingle(t *testing.T) {
	spec := renderedCluster(t, renderChart(t, nil))

	require.EqualValues(t, 1, spec["instances"])
	require.Equal(t, "ghcr.io/cloudnative-pg/postgresql:17.6", spec["imageName"])
	require.Equal(t, true, spec["enableSuperuserAccess"])
	require.Equal(t, "neutree-pg-superuser", spec["superuserSecret"].(map[string]interface{})["name"])
	require.NotContains(t, spec, "postgresql", "a single instance has no synchronous replication")

	initdb, _, err := unstructured.NestedMap(spec, "bootstrap", "initdb")
	require.NoError(t, err)
	require.Equal(t, "neutree", initdb["database"])
	require.Equal(t, "postgres", initdb["owner"])
	require.Equal(t, "neutree-pg-superuser", initdb["secret"].(map[string]interface{})["name"])
	require.Equal(t, "en_US.utf8", initdb["localeCollate"])
	require.Equal(t, "en_US.utf8", initdb["localeCType"])

	affinity := spec["affinity"].(map[string]interface{})
	require.Equal(t, "preferred", affinity["podAntiAffinityType"])
}

func TestDBClusterHA(t *testing.T) {
	spec := renderedCluster(t, renderChart(t, haValues(nil)))

	require.EqualValues(t, 3, spec["instances"])

	synchronous, _, err := unstructured.NestedMap(spec, "postgresql", "synchronous")
	require.NoError(t, err)
	require.Equal(t, map[string]interface{}{"method": "any", "number": int64(1), "dataDurability": "required"}, synchronous)

	affinity := spec["affinity"].(map[string]interface{})
	require.Equal(t, true, affinity["enablePodAntiAffinity"])
	require.Equal(t, "required", affinity["podAntiAffinityType"])
	require.Equal(t, "kubernetes.io/hostname", affinity["topologyKey"])

	// An explicit instance count wins over the HA default.
	spec = renderedCluster(t, renderChart(t, haValues(map[string]interface{}{
		"db": map[string]interface{}{"instances": 5},
	})))
	require.EqualValues(t, 5, spec["instances"])
}

func TestDBClusterScheduling(t *testing.T) {
	spec := renderedCluster(t, renderChart(t, schedulingOverrides()))
	affinity := spec["affinity"].(map[string]interface{})

	require.Equal(t, map[string]interface{}{"neutree.test/pool": "control-plane"}, affinity["nodeSelector"])
	require.Equal(t, []interface{}{testToleration}, affinity["tolerations"])
	require.Equal(t, testAffinity["nodeAffinity"], affinity["nodeAffinity"])
}

// Every database client goes through the read-write Service, and the clients
// that accept libpq keepalive parameters get them.
func TestDBConnections(t *testing.T) {
	for _, mode := range []map[string]interface{}{nil, haValues(nil)} {
		resources := renderChart(t, mode)

		manifest, err := yaml.Marshal(resources.Items)
		require.NoError(t, err)
		require.NotContains(t, string(manifest), "postgresql-service")

		env := func(kind, name, container, key string) string {
			t.Helper()

			for _, c := range podContainers(t, resources, kind, name) {
				if c.Name != container {
					continue
				}

				for _, e := range c.Env {
					if e.Name == key {
						return e.Value
					}
				}
			}

			t.Fatalf("%s/%s container %s has no %s", kind, name, container, key)

			return ""
		}

		require.Equal(t, "postgres://postgres:pgpassword@"+dbHost+":5432/neutree?"+keepaliveParams,
			env("Deployment", "neutree-postgrest", "postgrest", "PGRST_DB_URI"))
		require.Equal(t, "postgres://auth_admin:auth_admin_password@"+dbHost+":5432/neutree?sslmode=disable&connect_timeout=3",
			env("Deployment", "neutree-auth", "auth", "DATABASE_URL"))
		require.Equal(t, "5", env("Deployment", "neutree-auth", "auth", "GOTRUE_DB_MAX_IDLE_POOL_SIZE"))
		require.Equal(t, dbHost, env("Deployment", "neutree-postgresmeta", "neutree-postgresmeta", "PG_META_DB_HOST"))

		kongWorkloads := map[string][]string{
			"Deployment/neutree-kong":                    {"clear-stale-pid", "wait-for-db", "proxy"},
			"Job/neutree-kong-init-migrations-1":         {"kong-migrations"},
			"Job/neutree-kong-pre-upgrade-migrations-1":  {"kong-upgrade-migrations"},
			"Job/neutree-kong-post-upgrade-migrations-1": {"kong-post-upgrade-migrations"},
		}
		for workload, containers := range kongWorkloads {
			kind, name, _ := strings.Cut(workload, "/")
			for _, container := range containers {
				require.Equal(t, dbHost, env(kind, name, container, "KONG_PG_HOST"), workload)
				require.Equal(t, "5432", env(kind, name, container, "KONG_PG_PORT"), workload)
			}
		}

		var migration batchv1.Job
		renderedObject(t, resources, "Job", "neutree-migration-job-1", &migration)
		require.Contains(t, migration.Spec.Template.Spec.Containers[0].Args,
			"postgres://postgres:pgpassword@"+dbHost+":5432/neutree?sslmode=disable")

		var seed batchv1.Job
		renderedObject(t, resources, "Job", "neutree-post-migration-hook-job-1", &seed)
		require.Contains(t, seed.Spec.Template.Spec.Containers[0].Args[0],
			"postgres://postgres:pgpassword@"+dbHost+":5432/neutree?sslmode=disable&"+keepaliveParams)
	}
}

// Nothing may reach the new database before the migration Job has filled it.
func TestDBClientsWaitForMigration(t *testing.T) {
	resources := renderChart(t, nil)

	for _, workload := range []string{
		"Deployment/neutree-auth", "Deployment/neutree-postgrest", "Deployment/neutree-postgresmeta",
		"Deployment/neutree-kong", "Job/neutree-migration-job-1", "Job/neutree-post-migration-hook-job-1",
		"Job/neutree-kong-init-migrations-1", "Job/neutree-kong-pre-upgrade-migrations-1",
		"Job/neutree-kong-post-upgrade-migrations-1",
	} {
		kind, name, _ := strings.Cut(workload, "/")
		initContainers := podInitContainers(t, resources, kind, name)
		require.NotEmpty(t, initContainers, workload)
		require.Equal(t, "wait-pg-migration", initContainers[0].Name, workload)
		require.Equal(t, []string{"job-wr", "neutree-pg-migration-1", "-n", "default"}, initContainers[0].Args, workload)

		for _, c := range initContainers {
			for _, arg := range c.Args {
				require.NotContains(t, arg, "neutree-postgresql", "%s still waits for the old database pod", workload)
			}
		}
	}
}

func TestDBMigrationJob(t *testing.T) {
	resources := renderChart(t, nil)

	var job batchv1.Job
	renderedObject(t, resources, "Job", "neutree-pg-migration-1", &job)

	spec := job.Spec.Template.Spec
	require.Equal(t, corev1.RestartPolicyNever, spec.RestartPolicy)
	require.Equal(t, "neutree-sa", spec.ServiceAccountName)
	require.Equal(t, "ghcr.io/cloudnative-pg/postgresql:17.6", spec.Containers[0].Image)

	env := map[string]string{}
	for _, e := range spec.Containers[0].Env {
		env[e.Name] = e.Value
	}

	require.Equal(t, "neutree-pg", env["CLUSTER"])
	require.Equal(t, dbHost, env["TARGET_HOST"])
	// Offline rendering sees no Deployment from an earlier release.
	require.Empty(t, env["LEGACY_DEPLOYMENT"])
	require.Empty(t, env["LEGACY_SERVICE"])

	var scripts corev1.ConfigMap
	renderedObject(t, resources, "ConfigMap", "neutree-pg-migration", &scripts)
	require.Contains(t, scripts.Data, "migrate.sh")
	require.Contains(t, scripts.Data, "inventory.sql")

	// Without a live cluster there is nothing to keep from an earlier release.
	for _, r := range resources.Items {
		require.NotEqual(t, "neutree-postgresql", r.GetName(), r.GetKind())
	}
}

func podSpec(t *testing.T, resources *unstructured.UnstructuredList, kind, name string) corev1.PodSpec {
	t.Helper()

	switch kind {
	case "Deployment":
		var d appsv1.Deployment
		renderedObject(t, resources, kind, name, &d)

		return d.Spec.Template.Spec
	case "Job":
		var j batchv1.Job
		renderedObject(t, resources, kind, name, &j)

		return j.Spec.Template.Spec
	}

	t.Fatalf("unsupported kind %s", kind)

	return corev1.PodSpec{}
}

func podContainers(t *testing.T, resources *unstructured.UnstructuredList, kind, name string) []corev1.Container {
	spec := podSpec(t, resources, kind, name)

	return append(spec.InitContainers, spec.Containers...)
}

func podInitContainers(t *testing.T, resources *unstructured.UnstructuredList, kind, name string) []corev1.Container {
	return podSpec(t, resources, kind, name).InitContainers
}
