package neutree

import (
	"testing"

	"github.com/stretchr/testify/require"
	"helm.sh/helm/v3/pkg/chartutil"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// haDeployments maps each HA-capable values block to its Deployment name and
// the component label its pods carry. Vector, a StatefulSet in HA mode, has
// its own tests.
var haDeployments = map[string]struct{ deployment, component string }{
	"api":       {"neutree-api", "neutree-api"},
	"auth":      {"neutree-auth", "neutree-auth"},
	"postgrest": {"neutree-postgrest", "neutree-postgrest"},
	"core":      {"neutree-core", "neutree-core"},
	"kong":      {"neutree-kong", "neutree-kong"},
}

// haValues is values-ha.yaml with extra top-level blocks replacing its own.
func haValues(extra map[string]interface{}) map[string]interface{} {
	overlay, err := chartutil.ReadValuesFile("values-ha.yaml")
	if err != nil {
		panic(err)
	}

	values := overlay.AsMap()
	for k, v := range extra {
		values[k] = v
	}

	return values
}

func renderedPDBs(resources *unstructured.UnstructuredList) []unstructured.Unstructured {
	var out []unstructured.Unstructured

	for _, r := range resources.Items {
		if r.GetKind() == "PodDisruptionBudget" {
			out = append(out, r)
		}
	}

	return out
}

func requireHostnameAntiAffinity(t *testing.T, affinity *corev1.Affinity, component, name string) {
	t.Helper()

	require.NotNil(t, affinity, name)
	require.NotNil(t, affinity.PodAntiAffinity, name)

	terms := affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	require.Len(t, terms, 1, name)
	require.Equal(t, "kubernetes.io/hostname", terms[0].TopologyKey, name)
	require.Equal(t, map[string]string{"app.kubernetes.io/component": component},
		terms[0].LabelSelector.MatchLabels, name)
}

func TestHAModeDeployments(t *testing.T) {
	resources := renderChart(t, haValues(nil))

	for key, want := range haDeployments {
		var deployment appsv1.Deployment
		renderedObject(t, resources, "Deployment", want.deployment, &deployment)

		require.Equal(t, int32(3), *deployment.Spec.Replicas, key)
		require.Equal(t, want.component, deployment.Spec.Selector.MatchLabels["app.kubernetes.io/component"], key)

		strategy := deployment.Spec.Strategy
		require.Equal(t, appsv1.RollingUpdateDeploymentStrategyType, strategy.Type, key)
		require.Equal(t, intstr.FromInt32(0), *strategy.RollingUpdate.MaxSurge, key)
		require.Equal(t, intstr.FromInt32(1), *strategy.RollingUpdate.MaxUnavailable, key)

		requireHostnameAntiAffinity(t, deployment.Spec.Template.Spec.Affinity, want.component, key)
	}
}

func TestHAModePodDisruptionBudgets(t *testing.T) {
	resources := renderChart(t, haValues(nil))
	// One per stateless component, plus vector (TestVectorHAStatefulSet),
	// vmagent, the subcharts' vminsert, vmselect, vmstorage and grafana
	// (observability_ha_test.go), and the CloudNativePG operator
	// (TestHAModeOperatorReplicas).
	require.Len(t, renderedPDBs(resources), len(haDeployments)+7)

	for key, want := range haDeployments {
		var pdb policyv1.PodDisruptionBudget
		renderedObject(t, resources, "PodDisruptionBudget", want.deployment, &pdb)

		require.Nil(t, pdb.Spec.MinAvailable, key)
		require.Equal(t, intstr.FromInt32(1), *pdb.Spec.MaxUnavailable, key)
		require.Equal(t, map[string]string{"app.kubernetes.io/component": want.component},
			pdb.Spec.Selector.MatchLabels, key)
	}
}

func TestHAModeKeepsSchedulingValues(t *testing.T) {
	resources := renderChart(t, haValues(map[string]interface{}{
		"api": map[string]interface{}{
			"nodeSelector": testNodeSelector,
			"tolerations":  []interface{}{testToleration},
		},
	}))

	var deployment appsv1.Deployment
	renderedObject(t, resources, "Deployment", "neutree-api", &deployment)

	spec := deployment.Spec.Template.Spec
	require.Equal(t, "control-plane", spec.NodeSelector["neutree.test/pool"])
	require.Len(t, spec.Tolerations, 1)
	requireHostnameAntiAffinity(t, spec.Affinity, "neutree-api", "api")
}

func TestHAModeExplicitOverrides(t *testing.T) {
	overrides := map[string]interface{}{}
	for key := range haDeployments {
		overrides[key] = map[string]interface{}{"replicaCount": 2, "affinity": testAffinity}
	}

	resources := renderChart(t, haValues(overrides))

	for key, want := range haDeployments {
		var deployment appsv1.Deployment
		renderedObject(t, resources, "Deployment", want.deployment, &deployment)

		require.Equal(t, int32(2), *deployment.Spec.Replicas, key)

		affinity := deployment.Spec.Template.Spec.Affinity
		require.NotNil(t, affinity.NodeAffinity, key)
		require.Nil(t, affinity.PodAntiAffinity, key)
	}
}

func TestKongCacheWarmupInHAMode(t *testing.T) {
	warmup := func(values map[string]interface{}) (string, bool) {
		for _, c := range podSpec(t, renderChart(t, values), "Deployment", "neutree-kong").Containers {
			if c.Name != "proxy" {
				continue
			}

			for _, e := range c.Env {
				if e.Name == "KONG_DB_CACHE_WARMUP_ENTITIES" {
					return e.Value, true
				}
			}
		}

		return "", false
	}

	_, ok := warmup(nil)
	require.False(t, ok, "single mode keeps Kong's default warmup")

	value, ok := warmup(haValues(nil))
	require.True(t, ok)
	require.Equal(t, "services,consumers,keyauth_credentials,acls", value)
}

func TestSingleModeDefaults(t *testing.T) {
	resources := renderChart(t, nil)
	require.Empty(t, renderedPDBs(resources))

	for key, want := range haDeployments {
		var deployment appsv1.Deployment
		renderedObject(t, resources, "Deployment", want.deployment, &deployment)

		require.Equal(t, int32(1), *deployment.Spec.Replicas, key)
		require.Nil(t, deployment.Spec.Template.Spec.Affinity, key)

		// Only core pins a strategy outside HA mode; the rest keep the API default.
		if key == "core" {
			require.Equal(t, intstr.FromInt32(1), *deployment.Spec.Strategy.RollingUpdate.MaxSurge)
			require.Equal(t, intstr.FromInt32(0), *deployment.Spec.Strategy.RollingUpdate.MaxUnavailable)
		} else {
			require.Empty(t, deployment.Spec.Strategy.Type, key)
		}
	}

}

func TestSingleModeExplicitReplicaCount(t *testing.T) {
	overrides := map[string]interface{}{}
	for key := range haDeployments {
		overrides[key] = map[string]interface{}{"replicaCount": 2}
	}

	resources := renderChart(t, overrides)
	require.Empty(t, renderedPDBs(resources))

	for key, want := range haDeployments {
		var deployment appsv1.Deployment
		renderedObject(t, resources, "Deployment", want.deployment, &deployment)
		require.Equal(t, int32(2), *deployment.Spec.Replicas, key)
		require.Nil(t, deployment.Spec.Template.Spec.Affinity, key)
	}
}

func TestHAModeOperatorReplicas(t *testing.T) {
	var deployment appsv1.Deployment
	renderedObject(t, renderChart(t, haValues(nil)), "Deployment", "neutree-cloudnative-pg", &deployment)

	require.Equal(t, int32(2), *deployment.Spec.Replicas)
	require.Contains(t, deployment.Spec.Template.Spec.Containers[0].Args, "--leader-elect")

	terms := deployment.Spec.Template.Spec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	require.Len(t, terms, 1)
	require.Equal(t, "kubernetes.io/hostname", terms[0].TopologyKey)
	// Must select the operator pods themselves.
	require.Equal(t, terms[0].LabelSelector.MatchLabels["app.kubernetes.io/name"],
		deployment.Spec.Template.Labels["app.kubernetes.io/name"])

	var pdb policyv1.PodDisruptionBudget
	renderedObject(t, renderChart(t, haValues(nil)), "PodDisruptionBudget", "neutree-cnpg-operator", &pdb)
	require.Equal(t, 1, pdb.Spec.MaxUnavailable.IntValue())
	for k, v := range pdb.Spec.Selector.MatchLabels {
		require.Equal(t, v, deployment.Spec.Template.Labels[k], "PDB selector must match the operator pods")
	}

	renderedObject(t, renderChart(t, nil), "Deployment", "neutree-cloudnative-pg", &deployment)
	require.Equal(t, int32(1), *deployment.Spec.Replicas)
	require.Nil(t, deployment.Spec.Template.Spec.Affinity)
}

func TestVectorSingleModeDeployment(t *testing.T) {
	resources := renderChart(t, nil)

	var deployment appsv1.Deployment
	renderedObject(t, resources, "Deployment", "neutree-vector", &deployment)
	require.Equal(t, int32(1), *deployment.Spec.Replicas)
	require.Equal(t, appsv1.RecreateDeploymentStrategyType, deployment.Spec.Strategy.Type)
	require.Nil(t, deployment.Spec.Template.Spec.Affinity)

	var pvc corev1.PersistentVolumeClaim
	renderedObject(t, resources, "PersistentVolumeClaim", "neutree-vector-pvc", &pvc)
	require.Equal(t, "neutree-vector-pvc", vectorDataVolume(t, deployment.Spec.Template.Spec).PersistentVolumeClaim.ClaimName)
}

func TestVectorHAStatefulSet(t *testing.T) {
	resources := renderChart(t, haValues(map[string]interface{}{
		"vector": map[string]interface{}{
			"persistence": map[string]interface{}{"enabled": true, "size": "20Gi", "storageClassName": "fast"},
		},
	}))

	for _, r := range resources.Items {
		require.False(t, r.GetKind() == "Deployment" && r.GetName() == "neutree-vector", "no Vector Deployment in HA mode")
		require.False(t, r.GetKind() == "PersistentVolumeClaim" && r.GetName() == "neutree-vector-pvc", "no shared PVC in HA mode")
	}

	var sts appsv1.StatefulSet
	renderedObject(t, resources, "StatefulSet", "neutree-vector", &sts)
	require.Equal(t, int32(3), *sts.Spec.Replicas)
	require.Equal(t, "neutree-vector", sts.Spec.ServiceName)
	require.Equal(t, appsv1.ParallelPodManagement, sts.Spec.PodManagementPolicy)
	require.Equal(t, appsv1.RollingUpdateStatefulSetStrategyType, sts.Spec.UpdateStrategy.Type)
	requireHostnameAntiAffinity(t, sts.Spec.Template.Spec.Affinity, "neutree-vector", "vector")

	require.Len(t, sts.Spec.VolumeClaimTemplates, 1)
	claim := sts.Spec.VolumeClaimTemplates[0]
	require.Equal(t, "data", claim.Name)
	require.NotContains(t, claim.Labels, "helm.sh/chart", "claim templates are immutable")
	require.Equal(t, "fast", *claim.Spec.StorageClassName)
	require.Equal(t, "20Gi", claim.Spec.Resources.Requests.Storage().String())
	for _, v := range sts.Spec.Template.Spec.Volumes {
		require.NotEqual(t, "data", v.Name, "data comes from the claim template")
	}

	var pdb policyv1.PodDisruptionBudget
	renderedObject(t, resources, "PodDisruptionBudget", "neutree-vector", &pdb)
	require.Equal(t, intstr.FromInt32(1), *pdb.Spec.MaxUnavailable)
}

func TestVectorHAWithoutPersistence(t *testing.T) {
	resources := renderChart(t, haValues(map[string]interface{}{
		"vector": map[string]interface{}{
			"replicaCount": 2,
			"persistence":  map[string]interface{}{"enabled": false},
		},
	}))

	var sts appsv1.StatefulSet
	renderedObject(t, resources, "StatefulSet", "neutree-vector", &sts)
	require.Equal(t, int32(2), *sts.Spec.Replicas)
	require.Empty(t, sts.Spec.VolumeClaimTemplates)
	require.NotNil(t, vectorDataVolume(t, sts.Spec.Template.Spec).EmptyDir)
}

func vectorDataVolume(t *testing.T, spec corev1.PodSpec) corev1.Volume {
	t.Helper()

	for _, v := range spec.Volumes {
		if v.Name == "data" {
			return v
		}
	}

	require.Fail(t, "no data volume")

	return corev1.Volume{}
}

func TestKongGlobalRateLimit(t *testing.T) {
	env := func(values map[string]interface{}) map[string]string {
		out := map[string]string{}

		for _, c := range podSpec(t, renderChart(t, values), "Deployment", "neutree-kong").Containers {
			if c.Name == "proxy" {
				for _, e := range c.Env {
					out[e.Name] = e.Value
				}
			}
		}

		return out
	}

	off := env(nil)
	require.NotContains(t, off, "NEUTREE_GLOBAL_RATE_LIMIT")
	require.NotContains(t, off, "KONG_NGINX_MAIN_ENV")

	on := env(map[string]interface{}{"kong": map[string]interface{}{
		"globalRateLimit": map[string]interface{}{"enabled": true, "timeoutMs": 200, "cooldownSeconds": 5},
	}})
	require.Equal(t, "on", on["NEUTREE_GLOBAL_RATE_LIMIT"])
	require.Equal(t, "200", on["NEUTREE_GLOBAL_RATE_LIMIT_TIMEOUT_MS"])
	require.Equal(t, "5", on["NEUTREE_GLOBAL_RATE_LIMIT_COOLDOWN_SECONDS"])
	require.Contains(t, on["KONG_NGINX_MAIN_ENV"], "env NEUTREE_GLOBAL_RATE_LIMIT_TIMEOUT_MS")
}
