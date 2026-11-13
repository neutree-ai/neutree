package neutree

import (
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func renderedObject(t *testing.T, resources *unstructured.UnstructuredList, kind, name string, out interface{}) {
	t.Helper()

	for _, resource := range resources.Items {
		if resource.GetKind() == kind && resource.GetName() == name {
			require.NoError(t, runtime.DefaultUnstructuredConverter.FromUnstructured(resource.Object, out))
			return
		}
	}

	t.Fatalf("%s %s not rendered", kind, name)
}

func TestCoreDeploymentLeaderElection(t *testing.T) {
	var deployment appsv1.Deployment
	renderedObject(t, renderChart(t, nil), "Deployment", "neutree-core", &deployment)

	require.Equal(t, int32(1), *deployment.Spec.Replicas)

	// New and old pods must overlap so the old leader hands the Lease over.
	require.Equal(t, appsv1.RollingUpdateDeploymentStrategyType, deployment.Spec.Strategy.Type)
	require.Equal(t, intstr.FromInt32(1), *deployment.Spec.Strategy.RollingUpdate.MaxSurge)
	require.Equal(t, intstr.FromInt32(0), *deployment.Spec.Strategy.RollingUpdate.MaxUnavailable)

	container := deployment.Spec.Template.Spec.Containers[0]
	require.Contains(t, container.Args, "--leader-elect-lease-name=neutree-core-leader")

	fieldRefs := map[string]string{}
	for _, env := range container.Env {
		if env.ValueFrom != nil && env.ValueFrom.FieldRef != nil {
			fieldRefs[env.Name] = env.ValueFrom.FieldRef.FieldPath
		}
	}
	require.Equal(t, "metadata.name", fieldRefs["POD_NAME"])
	require.Equal(t, "metadata.namespace", fieldRefs["POD_NAMESPACE"])
}

func TestCoreDeploymentReplicaCount(t *testing.T) {
	var deployment appsv1.Deployment
	renderedObject(t, renderChart(t, map[string]interface{}{
		"core": map[string]interface{}{"replicaCount": 3},
	}), "Deployment", "neutree-core", &deployment)

	require.Equal(t, int32(3), *deployment.Spec.Replicas)
}

func TestCoreRoleAllowsLeaderLease(t *testing.T) {
	var role rbacv1.Role
	renderedObject(t, renderChart(t, nil), "Role", "neutree-role", &role)

	var leaseVerbs []string
	for _, rule := range role.Rules {
		for _, group := range rule.APIGroups {
			if group != "coordination.k8s.io" {
				continue
			}
			for _, resource := range rule.Resources {
				if resource == "leases" {
					leaseVerbs = append(leaseVerbs, rule.Verbs...)
				}
			}
		}
	}
	require.ElementsMatch(t, []string{"get", "create", "update"}, leaseVerbs)
}
