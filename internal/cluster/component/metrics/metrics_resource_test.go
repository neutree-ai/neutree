package metrics

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator"
	acceleratormocks "github.com/neutree-ai/neutree/internal/accelerator/mocks"
	"github.com/stretchr/testify/mock"
	"gotest.tools/v3/assert"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
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

func TestBuildMetricsResourcesIncludesNodeExporterDaemonSet(t *testing.T) {
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

	nodeExporter := findMetricsDaemonSet(t, objs, "neutree-node-exporter")
	assert.Equal(t, "test-namespace", nodeExporter.Namespace)
	assert.Equal(t, "neutree-node-exporter", nodeExporter.Labels["app"])
	assert.Equal(t, "test-image-prefix/prometheus/node-exporter:v1.8.2",
		nodeExporter.Spec.Template.Spec.Containers[0].Image)
	assert.Assert(t, nodeExporter.Spec.Template.Spec.HostNetwork)
	assert.Assert(t, nodeExporter.Spec.Template.Spec.HostPID)
	assert.Equal(t, "test-image-pull-secret", nodeExporter.Spec.Template.Spec.ImagePullSecrets[0].Name)
	assert.Equal(t, int32(19100), nodeExporter.Spec.Template.Spec.Containers[0].Ports[0].ContainerPort)
	assert.Assert(t, strings.Contains(strings.Join(nodeExporter.Spec.Template.Spec.Containers[0].Args, "\n"),
		"--web.listen-address=:19100"))
	assert.Assert(t, !strings.Contains(strings.Join(nodeExporter.Spec.Template.Spec.Containers[0].Args, "\n"),
		"--web.listen-address=:9100"))
	assert.Assert(t, strings.Contains(strings.Join(nodeExporter.Spec.Template.Spec.Containers[0].Args, "\n"),
		"--path.rootfs=/host"))
}

func TestBuildMetricsResourcesIncludesNeutreeNodeAgentDaemonSet(t *testing.T) {
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

	nodeAgent := findMetricsDaemonSet(t, objs, "neutree-node-agent")
	assert.Equal(t, "test-namespace", nodeAgent.Namespace)
	assert.Equal(t, "neutree-node-agent", nodeAgent.Labels["app"])
	assert.Equal(t, "test-image-prefix/neutree/neutree-node-agent:latest",
		nodeAgent.Spec.Template.Spec.Containers[0].Image)
	assert.Assert(t, nodeAgent.Spec.Template.Spec.HostNetwork)
	assert.Equal(t, "test-image-pull-secret", nodeAgent.Spec.Template.Spec.ImagePullSecrets[0].Name)

	args := strings.Join(nodeAgent.Spec.Template.Spec.Containers[0].Args, "\n")
	assert.Equal(t, int32(19101), nodeAgent.Spec.Template.Spec.Containers[0].Ports[0].ContainerPort)
	assert.Assert(t, strings.Contains(args, "--listen-address=:19101"))
	assert.Assert(t, strings.Contains(args, "--cluster=test-cluster"))
	assert.Assert(t, strings.Contains(args, "--workspace=test-workspace"))
	assert.Assert(t, strings.Contains(args, "--cluster-type=kubernetes"))
	assert.Assert(t, strings.Contains(args, "--node=$(NODE_NAME)"))
	assert.Assert(t, strings.Contains(args, "--node-ip=$(NODE_IP)"))
	assert.Assert(t, strings.Contains(args, "--node-exporter-url=http://127.0.0.1:19100/metrics"))
	assert.Assert(t, !strings.Contains(args, "--listen-address=:9101"))
	assert.Assert(t, !strings.Contains(args, "--node-exporter-url=http://127.0.0.1:9100/metrics"))
	assert.Assert(t, strings.Contains(args,
		"--kubelet-pod-resources-socket=/var/lib/kubelet/pod-resources/kubelet.sock"))

	envs := nodeAgent.Spec.Template.Spec.Containers[0].Env
	assert.Equal(t, "NODE_NAME", envs[0].Name)
	assert.Equal(t, "spec.nodeName", envs[0].ValueFrom.FieldRef.FieldPath)
	assert.Equal(t, "NODE_IP", envs[1].Name)
	assert.Equal(t, "status.hostIP", envs[1].ValueFrom.FieldRef.FieldPath)
	requireVolumeMount(t, nodeAgent, "kubelet-pod-resources", "/var/lib/kubelet/pod-resources")

	vmagentConfig := findMetricsConfigMap(t, objs, "vmagent-config").Data["prometheus.yml"]
	assert.Assert(t, strings.Contains(vmagentConfig, "job_name: 'neutree-node-agent'"))
	assert.Assert(t, strings.Contains(vmagentConfig, "label: app=neutree-node-agent"))
	assert.Assert(t, strings.Contains(vmagentConfig, "replacement: $1:19101"))
	assert.Assert(t, !strings.Contains(vmagentConfig, "replacement: $1:9101"))
}

func requireVolumeMount(t *testing.T, daemonSet *appsv1.DaemonSet, name, mountPath string) {
	t.Helper()

	if len(daemonSet.Spec.Template.Spec.Containers) == 0 {
		t.Fatal("expected daemonset to have containers")
	}
	for _, mount := range daemonSet.Spec.Template.Spec.Containers[0].VolumeMounts {
		if mount.Name == name && mount.MountPath == mountPath {
			return
		}
	}

	t.Fatalf("expected volume mount %s at %s", name, mountPath)
}

func TestBuildMetricsResourcesIncludesAcceleratorExporterFromPluginProfile(t *testing.T) {
	gin.SetMode(gin.TestMode)
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
		acceleratorMgr:  accelerator.NewManager(gin.New()),
	}

	objs, err := metricsCmpt.GetMetricsResources()
	if err != nil {
		t.Fatalf("Failed to build metrics resources: %v", err)
	}

	dcgm := findMetricsDaemonSet(t, objs, "nvidia-dcgm-exporter")
	assert.Equal(t, "nvidia-dcgm-exporter", dcgm.Labels["app"])
	assert.Equal(t, "test-image-prefix/nvidia/k8s/dcgm-exporter:3.3.9-3.6.1-ubuntu22.04",
		dcgm.Spec.Template.Spec.Containers[0].Image)
	assert.Equal(t, "test-image-pull-secret", dcgm.Spec.Template.Spec.ImagePullSecrets[0].Name)
	assert.Equal(t, int32(9400), dcgm.Spec.Template.Spec.Containers[0].Ports[0].ContainerPort)
	assert.DeepEqual(t,
		map[string]string{"nvidia.com/gpu.present": "true"},
		dcgm.Spec.Template.Spec.NodeSelector)
	assert.Assert(t, dcgm.Spec.Template.Spec.HostNetwork)
	requireContainerCapability(t, dcgm, "SYS_ADMIN")
	assert.Assert(t, dcgm.Spec.Template.Spec.Affinity == nil)

	config := findMetricsConfigMap(t, objs, "nvidia-dcgm-exporter-config")
	assert.Assert(t, strings.Contains(config.Data["default-counters.csv"], "DCGM_FI_DEV_GPU_UTIL"))

	vmagentConfig := findMetricsConfigMap(t, objs, "vmagent-config").Data["prometheus.yml"]
	assert.Assert(t, strings.Contains(vmagentConfig, "job_name: 'dcgm-exporter'"))
	assert.Assert(t, strings.Contains(vmagentConfig, "label: app=nvidia-dcgm-exporter"))

	nodeAgent := findMetricsDaemonSet(t, objs, "neutree-node-agent")
	args := strings.Join(nodeAgent.Spec.Template.Spec.Containers[0].Args, "\n")
	assert.Assert(t, strings.Contains(args, "--accelerator-exporter-url=http://127.0.0.1:9400/metrics"))
}

func TestBuildMetricsResourcesDoesNotParseDockerRunOptions(t *testing.T) {
	acceleratorMgr := &acceleratormocks.MockManager{}
	acceleratorMgr.On("SupportPlugins").Return([]string{"custom_gpu"})
	acceleratorMgr.On("GetAcceleratorProfile", mock.Anything, "custom_gpu").
		Return(&v1.AcceleratorProfile{
			AcceleratorType: "custom_gpu",
			Metrics: &v1.AcceleratorMetricsProfile{
				Exporter: &v1.AcceleratorExporterProfile{
					Kind:  "custom-exporter",
					Image: "example.com/custom/exporter:test",
					Port:  19090,
					Runtime: &v1.AcceleratorExporterRuntimeProfile{
						DockerRunOptions: []string{"--net=host", "--cap-add=SYS_ADMIN"},
					},
				},
			},
		}, true, nil)
	t.Cleanup(func() { acceleratorMgr.AssertExpectations(t) })

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
		acceleratorMgr:  acceleratorMgr,
	}

	objs, err := metricsCmpt.GetMetricsResources()
	if err != nil {
		t.Fatalf("Failed to build metrics resources: %v", err)
	}

	exporter := findMetricsDaemonSet(t, objs, "custom-gpu-custom-exporter")
	assert.Assert(t, !exporter.Spec.Template.Spec.HostNetwork)
	assert.Assert(t, exporter.Spec.Template.Spec.Containers[0].SecurityContext == nil)
}

func TestBuildMetricsResourcesSkipsAcceleratorExporterWithoutProvider(t *testing.T) {
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

	for _, obj := range objs.Items {
		assert.Assert(t, !(obj.GetKind() == "DaemonSet" && obj.GetName() == "nvidia-dcgm-exporter"))
		assert.Assert(t, !(obj.GetKind() == "ConfigMap" && obj.GetName() == "nvidia-dcgm-exporter-config"))
	}
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

func requireContainerCapability(t *testing.T, daemonSet *appsv1.DaemonSet, capability string) {
	t.Helper()

	securityContext := daemonSet.Spec.Template.Spec.Containers[0].SecurityContext
	if securityContext == nil || securityContext.Capabilities == nil {
		t.Fatalf("container securityContext capabilities are empty")
	}

	for _, candidate := range securityContext.Capabilities.Add {
		if string(candidate) == capability {
			return
		}
	}

	t.Fatalf("capability %s not found in %#v", capability, securityContext.Capabilities.Add)
}

func findMetricsDaemonSet(t *testing.T, objs *unstructured.UnstructuredList, name string) *appsv1.DaemonSet {
	t.Helper()

	for _, obj := range objs.Items {
		if obj.GetKind() == "DaemonSet" && obj.GetName() == name {
			objContent, _ := json.Marshal(obj.Object)
			daemonSet := &appsv1.DaemonSet{}
			if err := json.Unmarshal(objContent, daemonSet); err != nil {
				t.Fatalf("Failed to unmarshal DaemonSet %s: %v", name, err)
			}

			return daemonSet
		}
	}

	t.Fatalf("DaemonSet %s not found", name)

	return nil
}

func findMetricsConfigMap(t *testing.T, objs *unstructured.UnstructuredList, name string) *corev1.ConfigMap {
	t.Helper()

	for _, obj := range objs.Items {
		if obj.GetKind() == "ConfigMap" && obj.GetName() == name {
			objContent, _ := json.Marshal(obj.Object)
			configMap := &corev1.ConfigMap{}
			if err := json.Unmarshal(objContent, configMap); err != nil {
				t.Fatalf("Failed to unmarshal ConfigMap %s: %v", name, err)
			}

			return configMap
		}
	}

	t.Fatalf("ConfigMap %s not found", name)

	return nil
}
