package neutree

import (
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/yaml"
)

const vmFullname = "neutree-victoria-metrics-cluster"

// haOverlay loads values-ha.yaml the way `helm -f values-ha.yaml` would.
func haOverlay(t *testing.T) map[string]interface{} {
	t.Helper()

	return haValues(nil)
}

func hasObject(resources *unstructured.UnstructuredList, kind, name string) bool {
	for _, r := range resources.Items {
		if r.GetKind() == kind && r.GetName() == name {
			return true
		}
	}

	return false
}

func containerArgs(t *testing.T, resources *unstructured.UnstructuredList, kind, name string) []string {
	t.Helper()

	containers := podSpec(t, resources, kind, name).Containers
	require.NotEmpty(t, containers, name)

	return containers[0].Args
}

func TestVMAgentSingleModeUnchanged(t *testing.T) {
	resources := renderChart(t, nil)

	var deployment appsv1.Deployment
	renderedObject(t, resources, "Deployment", "neutree-vmagent", &deployment)
	require.Equal(t, int32(1), *deployment.Spec.Replicas)
	require.Equal(t, appsv1.RecreateDeploymentStrategyType, deployment.Spec.Strategy.Type)
	require.True(t, hasObject(resources, "PersistentVolumeClaim", "neutree-vmagent-pvc"))
	require.NotContains(t, containerArgs(t, resources, "Deployment", "neutree-vmagent"), "--promscrape.cluster.name=$(POD_NAME)")
	require.Nil(t, podSpec(t, resources, "Deployment", "neutree-vmagent").Containers[0].ReadinessProbe)
}

func TestVMAgentHAMode(t *testing.T) {
	resources := renderChart(t, haValues(nil))

	var deployment appsv1.Deployment
	renderedObject(t, resources, "Deployment", "neutree-vmagent", &deployment)
	require.Equal(t, int32(2), *deployment.Spec.Replicas)
	require.Equal(t, appsv1.RollingUpdateDeploymentStrategyType, deployment.Spec.Strategy.Type)
	require.Equal(t, intstr.FromInt32(0), *deployment.Spec.Strategy.RollingUpdate.MaxSurge)
	requireHostnameAntiAffinity(t, deployment.Spec.Template.Spec.Affinity, "neutree-vmagent", "vmagent")

	args := containerArgs(t, resources, "Deployment", "neutree-vmagent")
	require.Contains(t, args, "--promscrape.cluster.name=$(POD_NAME)")
	require.Contains(t, args, "--remoteWrite.tmpDataPath=/vmagentdata")
	require.Equal(t, "/health", deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.HTTPGet.Path)

	// The RWO volume would pin both replicas to one node; it is not rendered.
	require.False(t, hasObject(resources, "PersistentVolumeClaim", "neutree-vmagent-pvc"))

	for _, v := range deployment.Spec.Template.Spec.Volumes {
		if v.Name == "vmagentdata" {
			require.NotNil(t, v.EmptyDir)
			require.Nil(t, v.PersistentVolumeClaim)
		}
	}

	var pdb policyv1.PodDisruptionBudget
	renderedObject(t, resources, "PodDisruptionBudget", "neutree-vmagent", &pdb)
	require.Equal(t, map[string]string{"app.kubernetes.io/component": "neutree-vmagent"}, pdb.Spec.Selector.MatchLabels)
}

func TestVMAgentHAReplicaCountOverride(t *testing.T) {
	resources := renderChart(t, haValues(map[string]interface{}{
		"vmagent": map[string]interface{}{"replicaCount": 3},
	}))

	var deployment appsv1.Deployment
	renderedObject(t, resources, "Deployment", "neutree-vmagent", &deployment)
	require.Equal(t, int32(3), *deployment.Spec.Replicas)
}

func TestVectorTraceHeartbeat(t *testing.T) {
	render := func(values map[string]interface{}) (map[string]interface{}, []interface{}) {
		var cm corev1.ConfigMap
		renderedObject(t, renderChart(t, values), "ConfigMap", "neutree-vector", &cm)

		var config struct {
			Sources map[string]interface{}            `json:"sources"`
			Sinks   map[string]map[string]interface{} `json:"sinks"`
		}
		require.NoError(t, yaml.Unmarshal([]byte(cm.Data["vector.yml"]), &config))

		sink, ok := config.Sinks["ai_trace_victorialogs"]
		require.True(t, ok, "trace sink rendered")
		require.Equal(t, "disk", sink["buffer"].(map[string]interface{})["type"])

		return config.Sources, sink["inputs"].([]interface{})
	}

	for name, values := range map[string]map[string]interface{}{"single": nil, "ha": haValues(nil)} {
		sources, inputs := render(values)
		require.Contains(t, sources, "trace_buffer_heartbeat", name)
		require.Equal(t, []interface{}{"prepare_trace_payload", "prepare_trace_heartbeat"}, inputs, name)
	}
}

func TestHAOverlayVictoriaMetrics(t *testing.T) {
	resources := renderChart(t, haOverlay(t))

	for _, c := range []struct {
		kind, app string
		args      []string
	}{
		{"Deployment", "vminsert", []string{"--replicationFactor=2"}},
		{"Deployment", "vmselect", []string{"--replicationFactor=2", "--dedup.minScrapeInterval=5s"}},
		{"StatefulSet", "vmstorage", []string{"--dedup.minScrapeInterval=5s"}},
	} {
		name := vmFullname + "-" + c.app

		var obj struct {
			Spec struct {
				Replicas *int32                 `json:"replicas"`
				Template corev1.PodTemplateSpec `json:"template"`
			} `json:"spec"`
		}
		renderedObject(t, resources, c.kind, name, &obj)
		require.Equal(t, int32(3), *obj.Spec.Replicas, name)

		args := containerArgs(t, resources, c.kind, name)
		for _, a := range c.args {
			require.Contains(t, args, a, name)
		}

		terms := obj.Spec.Template.Spec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution
		require.Len(t, terms, 1, name)
		require.Equal(t, c.app, terms[0].LabelSelector.MatchLabels["app"], name)

		var pdb policyv1.PodDisruptionBudget
		renderedObject(t, resources, "PodDisruptionBudget", name, &pdb)
		require.Equal(t, intstr.FromInt32(1), *pdb.Spec.MaxUnavailable, name)
	}

	// Rolling must not surge: the anti-affinity leaves no node for a fourth pod.
	for _, app := range []string{"vminsert", "vmselect"} {
		var d appsv1.Deployment
		renderedObject(t, resources, "Deployment", vmFullname+"-"+app, &d)
		require.Equal(t, intstr.FromInt32(0), *d.Spec.Strategy.RollingUpdate.MaxSurge, app)
	}

	// vminsert and vmselect address all three storage nodes.
	storageNodes := 0

	for _, a := range containerArgs(t, resources, "Deployment", vmFullname+"-vminsert") {
		if len(a) > len("--storageNode=") && a[:len("--storageNode=")] == "--storageNode=" {
			storageNodes++
		}
	}

	require.Equal(t, 3, storageNodes)
}

func TestHAOverlayGrafanaDatabase(t *testing.T) {
	resources := renderChart(t, haOverlay(t))

	var deployment appsv1.Deployment
	renderedObject(t, resources, "Deployment", "neutree-grafana", &deployment)
	require.Equal(t, int32(2), *deployment.Spec.Replicas)
	require.Equal(t, appsv1.RollingUpdateDeploymentStrategyType, deployment.Spec.Strategy.Type)
	require.False(t, hasObject(resources, "PersistentVolumeClaim", "neutree-grafana"))

	env := map[string]corev1.EnvVar{}
	for _, e := range deployment.Spec.Template.Spec.Containers[0].Env {
		env[e.Name] = e
	}

	require.Equal(t, "postgres", env["GF_DATABASE_TYPE"].Value)

	for name, key := range map[string]string{"GF_DATABASE_HOST": "host", "GF_DATABASE_USER": "username", "GF_DATABASE_PASSWORD": "password"} {
		ref := env[name].ValueFrom.SecretKeyRef
		require.Equal(t, "neutree-grafana-db", ref.Name, name)
		require.Equal(t, key, ref.Key, name)
	}

	var secret corev1.Secret
	renderedObject(t, resources, "Secret", "neutree-grafana-db", &secret)
	require.Equal(t, "neutree-pg-rw:5432", secret.StringData["host"])
	require.Equal(t, "grafana", secret.StringData["username"])
	require.Len(t, secret.StringData["password"], 32)

	var pdb policyv1.PodDisruptionBudget
	renderedObject(t, resources, "PodDisruptionBudget", "neutree-grafana", &pdb)

	var database struct {
		Spec struct {
			Name    string `json:"name"`
			Owner   string `json:"owner"`
			Cluster struct {
				Name string `json:"name"`
			} `json:"cluster"`
		} `json:"spec"`
	}
	renderedObject(t, resources, "Database", "neutree-grafana", &database)
	require.Equal(t, "grafana", database.Spec.Name)
	require.Equal(t, "grafana", database.Spec.Owner)
	require.Equal(t, "neutree-pg", database.Spec.Cluster.Name)

	roles, _, err := unstructured.NestedSlice(renderedCluster(t, resources), "managed", "roles")
	require.NoError(t, err)
	require.Len(t, roles, 1)
	require.Equal(t, "grafana", roles[0].(map[string]interface{})["name"])
}

func TestGrafanaDatabaseExplicitPassword(t *testing.T) {
	resources := renderChart(t, map[string]interface{}{
		"grafanaDatabase": map[string]interface{}{"enabled": true, "password": "fixed"},
	})

	var secret corev1.Secret
	renderedObject(t, resources, "Secret", "neutree-grafana-db", &secret)
	require.Equal(t, "fixed", secret.StringData["password"])
}

func TestSingleModeObservabilityUnchanged(t *testing.T) {
	resources := renderChart(t, nil)

	require.False(t, hasObject(resources, "Secret", "neutree-grafana-db"))
	require.False(t, hasObject(resources, "Database", "neutree-grafana"))
	require.True(t, hasObject(resources, "PersistentVolumeClaim", "neutree-grafana"))

	_, found, err := unstructured.NestedSlice(renderedCluster(t, resources), "managed", "roles")
	require.NoError(t, err)
	require.False(t, found)

	for _, app := range []string{"vminsert", "vmselect"} {
		var deployment appsv1.Deployment
		renderedObject(t, resources, "Deployment", vmFullname+"-"+app, &deployment)
		require.Equal(t, int32(1), *deployment.Spec.Replicas, app)
	}
}

func TestHAWithoutOverlayFails(t *testing.T) {
	_, err := renderChartWithError(map[string]interface{}{"ha": map[string]interface{}{"enabled": true}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "victoria-metrics-cluster.vmstorage.replicaCount < 2")
	require.Contains(t, err.Error(), "grafana.replicas < 2")
	require.Contains(t, err.Error(), "-f <chart>/values-ha.yaml")

	// Subcharts turned off need nothing from the overlay.
	_, err = renderChartWithError(map[string]interface{}{
		"ha":                       map[string]interface{}{"enabled": true},
		"victoria-metrics-cluster": map[string]interface{}{"enabled": false},
		"grafana":                  map[string]interface{}{"enabled": false},
	})
	require.NoError(t, err)
}

func TestHAOverlayGrafanaMustUsePostgres(t *testing.T) {
	_, err := renderChartWithError(haValues(map[string]interface{}{
		"grafanaDatabase": map[string]interface{}{"enabled": false},
	}))
	require.Error(t, err)
	require.Contains(t, err.Error(), "Grafana does not use the PostgreSQL database")
}

func TestOverlayWithHADisabledFails(t *testing.T) {
	values := haValues(nil)
	values["ha"] = map[string]interface{}{"enabled": false, "overlay": true}

	_, err := renderChartWithError(values)
	require.Error(t, err)
	require.Contains(t, err.Error(), "values-ha.yaml is for ha.enabled=true")
}
