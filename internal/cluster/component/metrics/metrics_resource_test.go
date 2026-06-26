package metrics

import (
	"encoding/json"
	"strings"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"gotest.tools/v3/assert"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestBuildVMAgentDeployment(t *testing.T) {
	metricsCmpt := &MetricsComponent{
		cluster: &v1.Cluster{
			Metadata: &v1.Metadata{
				Name:      "test-cluster",
				Workspace: "test-workspace",
			},
			Spec: &v1.ClusterSpec{Version: "v1.1.0"},
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

func TestBuildVMAgentConfigIncludesHAMiMonitorScrape(t *testing.T) {
	metricsCmpt := &MetricsComponent{
		cluster: &v1.Cluster{
			Metadata: &v1.Metadata{
				Name:      "test-cluster",
				Workspace: "test-workspace",
			},
			Spec: &v1.ClusterSpec{
				Version: "v1.1.0",
				AcceleratorVirtualization: &v1.AcceleratorVirtualizationSpec{
					Enabled: true,
				},
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
		if obj.GetKind() == "ConfigMap" && obj.GetName() == "vmagent-config" {
			config, _, _ := unstructured.NestedString(obj.Object, "data", "prometheus.yml")
			assert.Assert(t, strings.Contains(config, "job_name: 'hami-vgpu-monitor'"))
			assert.Assert(t, strings.Contains(config, "label: app.kubernetes.io/component=hami-device-plugin"))
			assert.Assert(t, strings.Contains(config, "replacement: $1:9394"))
			assert.Assert(t, strings.Contains(config, "target_label: neutree_cluster"))
			assert.Assert(t, strings.Contains(config, "target_label: workspace"))
			assert.Assert(t, strings.Contains(config, "target_label: node"))
			assert.Assert(t, strings.Contains(config, "target_label: monitor_namespace"))
			assert.Assert(t, strings.Contains(config, "target_label: monitor_pod"))
			return
		}
	}

	t.Fatalf("vmagent config map not found in resources")
}

func TestBuildVMAgentConfigNormalizesSGLangMetricNames(t *testing.T) {
	metricsCmpt := &MetricsComponent{
		cluster: &v1.Cluster{
			Metadata: &v1.Metadata{
				Name:      "test-cluster",
				Workspace: "test-workspace",
			},
			Spec: &v1.ClusterSpec{Version: "v1.1.0"},
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
		if obj.GetKind() == "ConfigMap" && obj.GetName() == "vmagent-config" {
			config, _, _ := unstructured.NestedString(obj.Object, "data", "prometheus.yml")
			assert.Assert(t, strings.Contains(config, "job_name: 'neutree-inference'"))
			assert.Assert(t, strings.Contains(config, "metric_relabel_configs:"))
			assert.Assert(t, strings.Contains(config, "source_labels: [__name__]"))
			assert.Assert(t, strings.Contains(config, "regex: 'sglang[:_](.+)'"))
			assert.Assert(t, strings.Contains(config, "target_label: __name__"))
			assert.Assert(t, strings.Contains(config, "replacement: 'sglang_$1'"))
			return
		}
	}

	t.Fatalf("vmagent config map not found in resources")
}

func TestBuildMetricsResourcesSkipsKubeStateMetricsBeforeV110(t *testing.T) {
	metricsCmpt := &MetricsComponent{
		cluster: &v1.Cluster{
			Metadata: &v1.Metadata{
				Name:      "test-cluster",
				Workspace: "test-workspace",
			},
			Spec: &v1.ClusterSpec{Version: "v1.0.0"},
		},
		namespace:       "test-namespace",
		imagePrefix:     "test-image-prefix",
		imagePullSecret: "test-image-pull-secret",
	}

	objs, err := metricsCmpt.GetMetricsResources()
	if err != nil {
		t.Fatalf("Failed to build metrics resources: %v", err)
	}

	for _, obj := range objs.Items {
		assert.Assert(t, !(obj.GetKind() == "Deployment" && obj.GetName() == "neutree-kube-state-metrics"))
		assert.Assert(t, !(obj.GetKind() == "Service" && obj.GetName() == "neutree-kube-state-metrics"))
		assert.Assert(t, !(obj.GetKind() == "ServiceAccount" && obj.GetName() == "neutree-kube-state-metrics"))
		assert.Assert(t, !(obj.GetKind() == "Role" && obj.GetName() == "neutree-kube-state-metrics"))
		assert.Assert(t, !(obj.GetKind() == "RoleBinding" && obj.GetName() == "neutree-kube-state-metrics"))

		if obj.GetKind() == "ConfigMap" && obj.GetName() == "vmagent-config" {
			config, _, _ := unstructured.NestedString(obj.Object, "data", "prometheus.yml")
			assert.Assert(t, !strings.Contains(config, "job_name: 'neutree-kube-state-metrics'"))
		}
	}
}

func TestBuildVMAgentConfigSkipsHAMiMonitorScrapeWhenAcceleratorVirtualizationDisabled(t *testing.T) {
	metricsCmpt := &MetricsComponent{
		cluster: &v1.Cluster{
			Metadata: &v1.Metadata{
				Name:      "test-cluster",
				Workspace: "test-workspace",
			},
			Spec: &v1.ClusterSpec{Version: "v1.1.0"},
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
		if obj.GetKind() == "ConfigMap" && obj.GetName() == "vmagent-config" {
			config, _, _ := unstructured.NestedString(obj.Object, "data", "prometheus.yml")
			assert.Assert(t, !strings.Contains(config, "job_name: 'hami-vgpu-monitor'"))
			return
		}
	}

	t.Fatalf("vmagent config map not found in resources")
}

func TestBuildVMAgentConfigIncludesHAMiMonitorScrapeBeforeV110WhenAcceleratorVirtualizationEnabled(t *testing.T) {
	metricsCmpt := &MetricsComponent{
		cluster: &v1.Cluster{
			Metadata: &v1.Metadata{
				Name:      "test-cluster",
				Workspace: "test-workspace",
			},
			Spec: &v1.ClusterSpec{
				Version: "v1.0.0",
				AcceleratorVirtualization: &v1.AcceleratorVirtualizationSpec{
					Enabled: true,
				},
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
		if obj.GetKind() == "ConfigMap" && obj.GetName() == "vmagent-config" {
			config, _, _ := unstructured.NestedString(obj.Object, "data", "prometheus.yml")
			assert.Assert(t, strings.Contains(config, "job_name: 'hami-vgpu-monitor'"))
			assert.Assert(t, !strings.Contains(config, "job_name: 'neutree-kube-state-metrics'"))
			return
		}
	}

	t.Fatalf("vmagent config map not found in resources")
}

func TestBuildMetricsResourcesIncludesKubeStateMetrics(t *testing.T) {
	metricsCmpt := &MetricsComponent{
		cluster: &v1.Cluster{
			Metadata: &v1.Metadata{
				Name:      "test-cluster",
				Workspace: "test-workspace",
			},
			Spec: &v1.ClusterSpec{Version: "v1.1.0"},
		},
		namespace:       "test-namespace",
		imagePrefix:     "test-image-prefix",
		imagePullSecret: "test-image-pull-secret",
	}

	objs, err := metricsCmpt.GetMetricsResources()
	if err != nil {
		t.Fatalf("Failed to build metrics resources: %v", err)
	}

	var foundDeployment bool
	var foundService bool
	var foundServiceAccount bool
	var foundRole bool
	var foundRoleBinding bool

	for _, obj := range objs.Items {
		switch {
		case obj.GetKind() == "Deployment" && obj.GetName() == "neutree-kube-state-metrics":
			foundDeployment = true
			objContent, _ := json.Marshal(obj.Object)
			deployment := &appsv1.Deployment{}
			err = json.Unmarshal(objContent, deployment)
			if err != nil {
				t.Fatalf("Failed to unmarshal kube-state-metrics deployment: %v", err)
			}
			assert.Equal(t, "test-namespace", deployment.GetNamespace())
			assert.Equal(t, "test-image-prefix/kube-state-metrics/kube-state-metrics:v2.15.0",
				deployment.Spec.Template.Spec.Containers[0].Image)
			assert.Equal(t, "test-image-pull-secret", deployment.Spec.Template.Spec.ImagePullSecrets[0].Name)
			args := strings.Join(deployment.Spec.Template.Spec.Containers[0].Args, "\n")
			assert.Assert(t, strings.Contains(args, "--resources=pods"))
			assert.Assert(t, strings.Contains(args, "--namespaces=test-namespace"))
			assert.Assert(t, strings.Contains(args, "--metric-labels-allowlist=pods=[app,cluster,workspace,endpoint,engine,engine_version]"))
		case obj.GetKind() == "Service" && obj.GetName() == "neutree-kube-state-metrics":
			foundService = true
		case obj.GetKind() == "ServiceAccount" && obj.GetName() == "neutree-kube-state-metrics":
			foundServiceAccount = true
		case obj.GetKind() == "Role" && obj.GetName() == "neutree-kube-state-metrics":
			foundRole = true
		case obj.GetKind() == "RoleBinding" && obj.GetName() == "neutree-kube-state-metrics":
			foundRoleBinding = true
		}
	}

	assert.Assert(t, foundDeployment, "kube-state-metrics deployment not found")
	assert.Assert(t, foundService, "kube-state-metrics service not found")
	assert.Assert(t, foundServiceAccount, "kube-state-metrics service account not found")
	assert.Assert(t, foundRole, "kube-state-metrics role not found")
	assert.Assert(t, foundRoleBinding, "kube-state-metrics role binding not found")
}

func TestBuildVMAgentConfigIncludesKubeStateMetricsScrape(t *testing.T) {
	metricsCmpt := &MetricsComponent{
		cluster: &v1.Cluster{
			Metadata: &v1.Metadata{
				Name:      "test-cluster",
				Workspace: "test-workspace",
			},
			Spec: &v1.ClusterSpec{Version: "v1.1.0"},
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
		if obj.GetKind() == "ConfigMap" && obj.GetName() == "vmagent-config" {
			config, _, _ := unstructured.NestedString(obj.Object, "data", "prometheus.yml")
			assert.Assert(t, strings.Contains(config, "job_name: 'neutree-kube-state-metrics'"))
			assert.Assert(t, strings.Contains(config, "label: app=neutree-kube-state-metrics"))
			assert.Assert(t, strings.Contains(config, "target_label: monitor_namespace"))
			assert.Assert(t, strings.Contains(config, "target_label: monitor_pod"))
			assert.Assert(t, strings.Contains(config, "target_label: neutree_cluster"))
			assert.Assert(t, strings.Contains(config, "target_label: workspace"))
			return
		}
	}

	t.Fatalf("vmagent config map not found in resources")
}
