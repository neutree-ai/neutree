package metrics

import (
	"encoding/json"
	"strings"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"gotest.tools/v3/assert"
	appsv1 "k8s.io/api/apps/v1"
)

func TestBuildVMAgentDeployment(t *testing.T) {
	metricsCmpt := &MetricsComponent{
		cluster: &v1.Cluster{
			Metadata: &v1.Metadata{
				Name:      "test-cluster",
				Workspace: "test-workspace",
			},
		},
		namespace:       "test-namespace",
		imagePrefix:     "test-image-prefix",
		imagePullSecret: "test-image-pull-secret",
	}

	objs, err := metricsCmpt.GetMetricsResources()
	if err != nil {
		t.Fatalf("Failed to build vmagent deployment: %v", err)
	}

	for _, obj := range objs.Items {
		if obj.GetObjectKind().GroupVersionKind().Kind == "Deployment" && obj.GetName() == "vmagent" {
			objContent, _ := json.Marshal(obj.Object)
			deployment := &appsv1.Deployment{}
			err = json.Unmarshal(objContent, deployment)
			if err != nil {
				t.Fatalf("Failed to unmarshal deployment: %v", err)
			}

			assert.Equal(t, "test-namespace", deployment.GetNamespace(), "Deployment namespace mismatch")
			assert.Equal(t, "vmagent", deployment.GetName(), "Deployment name mismatch")
			assert.Equal(t, "vmagent", deployment.GetLabels()["app"], "Deployment app label mismatch")
			assert.Equal(t, int32(1), *deployment.Spec.Replicas, "Deployment replicas mismatch")
			assert.Equal(t, "test-image-prefix/victoriametrics/vmagent:v1.115.0", deployment.Spec.Template.Spec.Containers[0].Image, "Deployment image mismatch")
			assert.Equal(t, "test-image-pull-secret", deployment.Spec.Template.Spec.ImagePullSecrets[0].Name, "Deployment image pull secret mismatch")
			return
		}
	}

	t.Fatalf("vmagent deployment not found in resources")
}

func TestBuildVMAgentConfigScrapesPDContainersWithRoleAndRank(t *testing.T) {
	metricsCmpt := &MetricsComponent{
		cluster: &v1.Cluster{
			Metadata: &v1.Metadata{
				Name:      "test-cluster",
				Workspace: "test-workspace",
			},
		},
		namespace:       "test-namespace",
		imagePrefix:     "test-image-prefix",
		imagePullSecret: "test-image-pull-secret",
	}

	objs, err := metricsCmpt.GetMetricsResources()
	if err != nil {
		t.Fatalf("Failed to build vmagent resources: %v", err)
	}

	for _, obj := range objs.Items {
		if obj.GetObjectKind().GroupVersionKind().Kind != "ConfigMap" || obj.GetName() != "vmagent-config" {
			continue
		}

		data, ok := obj.Object["data"].(map[string]interface{})
		if !ok {
			t.Fatalf("vmagent config data missing or wrong type: %#v", obj.Object["data"])
		}
		prometheusYAML, ok := data["prometheus.yml"].(string)
		if !ok {
			t.Fatalf("prometheus.yml missing or wrong type: %#v", data["prometheus.yml"])
		}

		for _, want := range []string{
			"job_name: 'neutree-inference-pd'",
			"__meta_kubernetes_pod_label_neutree_io_component",
			"__meta_kubernetes_pod_container_port_name",
			"__meta_kubernetes_pod_container_port_number",
			"target_label: role",
			"replacement: prefill",
			"replacement: decode",
			"replacement: router",
			"target_label: rank",
			"target_label: container",
		} {
			if !strings.Contains(prometheusYAML, want) {
				t.Fatalf("prometheus.yml missing %q:\n%s", want, prometheusYAML)
			}
		}

		if !strings.Contains(prometheusYAML, "action: drop") ||
			!strings.Contains(prometheusYAML, "regex: pd-collocated") {
			t.Fatalf("default inference scrape must drop PD pods to avoid duplicate router-only targets:\n%s", prometheusYAML)
		}

		return
	}

	t.Fatalf("vmagent-config ConfigMap not found")
}
