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

// dbResources returns the CloudNativePG objects the chart defines, as stored
// in the resources ConfigMap. When helm also renders them directly, the two
// must be identical.
func dbResources(t *testing.T, resources *unstructured.UnstructuredList) []unstructured.Unstructured {
	t.Helper()

	var configMap corev1.ConfigMap
	renderedObject(t, resources, "ConfigMap", "neutree-pg-resources", &configMap)

	var out []unstructured.Unstructured

	for key, manifest := range configMap.Data {
		data, err := yaml.YAMLToJSON([]byte(manifest))
		require.NoError(t, err, key)

		var object unstructured.Unstructured
		require.NoError(t, object.UnmarshalJSON(data), key)

		for _, r := range resources.Items {
			if r.GetKind() == object.GetKind() && r.GetName() == object.GetName() {
				require.Equal(t, object.Object, r.Object, key)
			}
		}

		out = append(out, object)
	}

	return out
}

func dbResource(t *testing.T, resources *unstructured.UnstructuredList, kind string) (unstructured.Unstructured, bool) {
	t.Helper()

	for _, r := range dbResources(t, resources) {
		if r.GetKind() == kind {
			return r, true
		}
	}

	return unstructured.Unstructured{}, false
}

func renderedCluster(t *testing.T, resources *unstructured.UnstructuredList) map[string]interface{} {
	t.Helper()

	cluster, found := dbResource(t, resources, "Cluster")
	require.True(t, found, "no CloudNativePG Cluster rendered")
	require.Equal(t, "postgresql.cnpg.io/v1", cluster.GetAPIVersion())
	require.Equal(t, "neutree-pg", cluster.GetName())

	spec, _, err := unstructured.NestedMap(cluster.Object, "spec")
	require.NoError(t, err)

	return spec
}

func TestDBRequiresCloudNativePG(t *testing.T) {
	release := chartutil.ReleaseOptions{Name: "neutree", Namespace: "default", Revision: 1, IsInstall: true}
	external := map[string]interface{}{"cnpg": map[string]interface{}{"enabled": false}}

	// The bundled operator brings the API with it.
	_, err := renderChartWithAPIs(nil, release, false)
	require.NoError(t, err)

	_, err = renderChartWithAPIs(external, release, false)
	require.ErrorContains(t, err, "postgresql.cnpg.io/v1")
	require.ErrorContains(t, err, "db.operatorCheck=false")

	resources, err := renderChartWithAPIs(map[string]interface{}{
		"cnpg": map[string]interface{}{"enabled": false},
		"db":   map[string]interface{}{"operatorCheck": false},
	}, release, false)
	require.NoError(t, err)
	renderedCluster(t, resources)
}

func migrationEnv(t *testing.T, resources *unstructured.UnstructuredList) map[string]string {
	t.Helper()

	var job batchv1.Job
	renderedObject(t, resources, "Job", "neutree-pg-migration-1", &job)

	env := map[string]string{}
	for _, e := range job.Spec.Template.Spec.Containers[0].Env {
		env[e.Name] = e.Value
	}

	return env
}

func TestCNPGOperatorSubchart(t *testing.T) {
	resources := renderChart(t, nil)

	var deployment appsv1.Deployment
	renderedObject(t, resources, "Deployment", "neutree-cloudnative-pg", &deployment)

	require.Equal(t, "cloudnative-pg", deployment.Labels["app.kubernetes.io/name"],
		"the operator finds its own Deployment by this label")

	container := deployment.Spec.Template.Spec.Containers[0]
	require.Equal(t, "ghcr.io/cloudnative-pg/cloudnative-pg:1.30.1", container.Image)

	env := map[string]string{}
	for _, e := range container.Env {
		env[e.Name] = e.Value
	}

	require.Equal(t, "default", env["WATCH_NAMESPACE"], "the operator watches the release namespace only")
	require.Equal(t, container.Image, env["OPERATOR_IMAGE_NAME"])

	var config corev1.ConfigMap
	renderedObject(t, resources, "ConfigMap", "cnpg-controller-manager-config", &config)
	require.Equal(t, "default", config.Data["WATCH_NAMESPACE"])
	require.Equal(t, "true", config.Data["ENABLE_INSTANCE_MANAGER_INPLACE_UPDATES"])

	require.True(t, hasObject(resources, "Role", "neutree-cloudnative-pg"))
	require.True(t, hasObject(resources, "CustomResourceDefinition", "clusters.postgresql.cnpg.io"))
	require.True(t, hasObject(resources, "MutatingWebhookConfiguration", "cnpg-mutating-webhook-configuration"))

	// Offline installs point the operator image at the mirror.
	resources = renderChart(t, map[string]interface{}{
		"cnpg": map[string]interface{}{"image": map[string]interface{}{"repository": "mirror.example/cloudnative-pg/cloudnative-pg"}},
	})
	renderedObject(t, resources, "Deployment", "neutree-cloudnative-pg", &deployment)
	require.Equal(t, "mirror.example/cloudnative-pg/cloudnative-pg:1.30.1", deployment.Spec.Template.Spec.Containers[0].Image)
}

// Without a live Cluster (first install, or the first upgrade from the
// PostgreSQL Deployment), helm cannot create the Cluster next to the operator
// it installs; the migration Job does.
func TestDBResourcesDeferredWithBundledOperator(t *testing.T) {
	resources := renderChart(t, haValues(nil))

	require.False(t, hasObject(resources, "Cluster", "neutree-pg"))
	require.Len(t, dbResources(t, resources), 1)
	require.Equal(t, "true", migrationEnv(t, resources)["CREATE_RESOURCES"])
	require.Equal(t, "neutree-pg-resources", migrationEnv(t, resources)["RESOURCES_CONFIGMAP"])

	var job batchv1.Job
	renderedObject(t, resources, "Job", "neutree-pg-migration-1", &job)

	mounted := false

	for _, v := range job.Spec.Template.Spec.Volumes {
		if v.ConfigMap != nil && v.ConfigMap.Name == "neutree-pg-resources" {
			mounted = true
		}
	}

	require.True(t, mounted, "the Job reads the manifests from the resources ConfigMap")
}

func TestDBResourcesWithExternalOperator(t *testing.T) {
	resources := renderChart(t, haValues(map[string]interface{}{
		"cnpg": map[string]interface{}{"enabled": false},
	}))

	require.True(t, hasObject(resources, "Cluster", "neutree-pg"))
	require.Len(t, dbResources(t, resources), 1)
	require.Equal(t, "", migrationEnv(t, resources)["CREATE_RESOURCES"])
	require.False(t, hasObject(resources, "Deployment", "neutree-cloudnative-pg"))
	require.False(t, hasObject(resources, "CustomResourceDefinition", "clusters.postgresql.cnpg.io"))
}

// helm must never delete the Cluster itself (a rollback to the revision that
// deferred it would); the resources ConfigMap owns it.
func TestDBResourcesAnnotations(t *testing.T) {
	for _, object := range dbResources(t, renderChart(t, haValues(nil))) {
		require.Equal(t, map[string]string{
			"helm.sh/resource-policy":        "keep",
			"meta.helm.sh/release-name":      "neutree",
			"meta.helm.sh/release-namespace": "default",
		}, object.GetAnnotations(), object.GetKind())
	}
}

func TestDBClusterSingle(t *testing.T) {
	spec := renderedCluster(t, renderChart(t, nil))

	require.EqualValues(t, 1, spec["instances"])
	require.Equal(t, "ghcr.io/cloudnative-pg/postgresql:17.6", spec["imageName"])
	require.Equal(t, true, spec["enableSuperuserAccess"])
	require.Equal(t, "neutree-pg-superuser", spec["superuserSecret"].(map[string]interface{})["name"])
	require.NotContains(t, spec, "postgresql", "a single instance has no synchronous replication")
	require.Equal(t, false, spec["enablePDB"], "a lone instance must stay evictable")
	require.EqualValues(t, 10, spec["smartShutdownTimeout"])
	require.NotContains(t, spec, "primaryUpdateMethod")

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
	require.Equal(t, true, spec["enablePDB"])
	require.Equal(t, "switchover", spec["primaryUpdateMethod"])

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

	check := func(workload, initName string, args []string) {
		t.Helper()

		kind, name, _ := strings.Cut(workload, "/")
		initContainers := podInitContainers(t, resources, kind, name)
		require.NotEmpty(t, initContainers, workload)
		require.Equal(t, initName, initContainers[0].Name, workload)

		if args != nil {
			require.Equal(t, args, initContainers[0].Args, workload)
		}

		for _, c := range initContainers {
			for _, arg := range append(c.Args, c.Command...) {
				require.NotContains(t, arg, "neutree-postgresql", "%s still waits for the old database pod", workload)
			}
		}
	}

	// Jobs are per revision anyway; they wait for this revision's migration Job.
	for _, job := range []string{
		"Job/neutree-migration-job-1", "Job/neutree-post-migration-hook-job-1",
		"Job/neutree-kong-init-migrations-1", "Job/neutree-kong-pre-upgrade-migrations-1",
		"Job/neutree-kong-post-upgrade-migrations-1",
	} {
		check(job, "wait-pg-migration", []string{"job-wr", "neutree-pg-migration-1", "-n", "default"})
	}

	// Deployments ask the database for the completion record.
	for _, deployment := range []string{
		"Deployment/neutree-auth", "Deployment/neutree-postgrest", "Deployment/neutree-kong",
	} {
		check(deployment, "wait-pg-ready", nil)

		kind, name, _ := strings.Cut(deployment, "/")
		wait := podInitContainers(t, resources, kind, name)[0]
		require.Contains(t, strings.Join(wait.Command, " "), "neutree_pg_migration", deployment)
	}
}

// A helm upgrade that changes nothing must not restart the database clients.
func TestDBDeploymentsStableAcrossRevisions(t *testing.T) {
	render := func(revision int) *unstructured.UnstructuredList {
		resources, err := renderChartForRelease(nil, chartutil.ReleaseOptions{
			Name: "neutree", Namespace: "default", Revision: revision, IsUpgrade: revision > 1, IsInstall: revision == 1,
		})
		require.NoError(t, err)

		return resources
	}

	first, second := render(1), render(2)

	for _, name := range []string{"neutree-auth", "neutree-postgrest", "neutree-kong"} {
		var a, b appsv1.Deployment
		renderedObject(t, first, "Deployment", name, &a)
		renderedObject(t, second, "Deployment", name, &b)
		require.Equal(t, a.Spec.Template, b.Spec.Template, name)
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
	for _, f := range []string{"migrate-k8s.sh", "pg-copy.sh", "inventory.sql"} {
		require.Contains(t, scripts.Data, f)
	}

	require.Equal(t, []string{"bash", "/scripts/migrate-k8s.sh"}, spec.Containers[0].Command)

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
	case "StatefulSet":
		var s appsv1.StatefulSet
		renderedObject(t, resources, kind, name, &s)

		return s.Spec.Template.Spec
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
