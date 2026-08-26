package hami

import (
	"context"
	"encoding/base64"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kubeversion "k8s.io/apimachinery/pkg/version"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
	"github.com/neutree-ai/neutree/internal/accelerator/resourceparser"
	"github.com/neutree-ai/neutree/pkg/accelerator"
)

var testHAMiNodeAnnotations = []string{
	"hami.io/node-nvidia-register",
	"hami.io/node-nvidia-score",
	"hami.io/node-handshake",
	"hami.io/mutex.lock",
}

func TestHAMiComponentResources(t *testing.T) {
	component := NewHAMiComponent(newTestCluster(), "neutree-system", "registry.example.com/neutree/",
		"image-pull-secret", v1.KubernetesClusterConfig{}, newHAMiFakeClient(t))

	objs, err := component.renderResources(nvidiaDevicePluginNodeScopePlan())

	require.NoError(t, err)
	assertHasObject(t, objs.Items, "ServiceAccount", "hami-scheduler")
	assertHasObject(t, objs.Items, "ServiceAccount", "hami-device-plugin")
	assertHasObject(t, objs.Items, "ClusterRole", "hami-scheduler")
	assertHasObject(t, objs.Items, "ClusterRoleBinding", "hami-scheduler")
	assertHasObject(t, objs.Items, "Deployment", "hami-scheduler")
	assertHasObject(t, objs.Items, "DaemonSet", "hami-device-plugin")
	assertHasObject(t, objs.Items, "ConfigMap", "hami-device-plugin")
	assertHasObject(t, objs.Items, "ConfigMap", "hami-scheduler-device")
	assertHasObject(t, objs.Items, "Service", MonitorServiceName)
}

func TestHAMiComponentDefaultResourcesDoNotRenderNVIDIADevicePlugin(t *testing.T) {
	component := NewHAMiComponent(newTestCluster(), "neutree-system", "registry.example.com/neutree/",
		"image-pull-secret", v1.KubernetesClusterConfig{}, newHAMiFakeClient(t))

	objs, err := component.renderResources(defaultNodeScopePlan())

	require.NoError(t, err)
	assertNoObject(t, objs.Items, "DaemonSet", DevicePluginDaemonSetName)
}

func TestHAMiComponentPluginPatchControlsDevicePluginValues(t *testing.T) {
	component := NewHAMiComponent(newTestCluster(), "neutree-system", "registry.example.com/neutree/",
		"image-pull-secret", v1.KubernetesClusterConfig{}, newHAMiFakeClient(t))
	values := component.buildChartValues(NodeScopePlan{
		DisabledNodes: []string{"disabled-node"},
		ConfigPatch: map[string]interface{}{
			"devicePlugin": map[string]interface{}{
				"enabled": true,
				"nvidiaNodeSelector": map[string]interface{}{
					"example.com/plugin-owned": "true",
				},
			},
		},
	})

	devicePlugin := nestedMap(t, values, "devicePlugin")
	assert.Equal(t, true, devicePlugin["enabled"])
	assert.Equal(t, map[string]interface{}{
		"example.com/plugin-owned": "true",
	}, devicePlugin["nvidiaNodeSelector"])
}

func TestHAMiComponentResourcesAppendOwnerDevicePluginTemplate(t *testing.T) {
	component := NewHAMiComponent(newTestCluster(), "neutree-system", "registry.example.com/neutree/",
		"image-pull-secret", v1.KubernetesClusterConfig{}, newHAMiFakeClient(t))
	plan := defaultNodeScopePlan()
	plan.DevicePluginTemplate = &accelerator.DevicePluginTemplate{Manifest: `
apiVersion: v1
kind: ConfigMap
metadata:
  name: owner-device-plugin
  namespace: {{ .Namespace }}
  labels:
    scope-key: {{ .NodeScopeLabel.Key }}
    scope-enabled: "{{ .NodeScopeLabel.EnabledValue }}"
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: owner-device-plugin
  namespace: {{ .Namespace }}
`}

	objs, err := component.renderResources(plan)

	require.NoError(t, err)
	assertHasObject(t, objs.Items, "Deployment", SchedulerName)
	configMap := findObject(t, objs.Items, "ConfigMap", "owner-device-plugin")
	assert.Equal(t, "neutree-system", configMap.GetNamespace())
	assert.Equal(t, plugin.NvidiaGPUVirtualizationLabelKey, configMap.GetLabels()["scope-key"])
	assert.Equal(t, "true", configMap.GetLabels()["scope-enabled"])
	assertHasObject(t, objs.Items, "ServiceAccount", "owner-device-plugin")
}

func TestHAMiComponentOwnerTemplateUsesSharedApplyAndPruneLifecycle(t *testing.T) {
	const ownerResourceName = "owner-device-plugin-lifecycle"

	ctx := context.Background()
	cluster := newTestCluster()
	fakeClient := &hamiFakeApplyClient{Client: newHAMiFakeClient(t)}
	component := NewHAMiComponent(cluster, "neutree-system", "registry.example.com/neutree/",
		"image-pull-secret", v1.KubernetesClusterConfig{}, fakeClient)
	plan := defaultNodeScopePlan()
	plan.DevicePluginTemplate = &accelerator.DevicePluginTemplate{Manifest: `
apiVersion: v1
kind: ConfigMap
metadata:
  name: owner-device-plugin-lifecycle
  namespace: {{ .Namespace }}
data:
  scope-key: {{ .NodeScopeLabel.Key }}
`}

	require.NoError(t, component.ApplyResources(ctx, plan))

	applied := &corev1.ConfigMap{}
	require.NoError(t, fakeClient.Get(ctx, client.ObjectKey{
		Name:      ownerResourceName,
		Namespace: "neutree-system",
	}, applied))
	assert.Equal(t, plugin.NvidiaGPUVirtualizationLabelKey, applied.Data["scope-key"])
	assert.Equal(t, ManagedComponentLabelValue, applied.Labels[ManagedComponentLabelKey])
	assert.Equal(t, cluster.Metadata.Name, applied.Labels[v1.NeutreeClusterLabelKey])
	assert.Equal(t, cluster.Metadata.Workspace, applied.Labels[v1.NeutreeClusterWorkspaceLabelKey])
	assert.Equal(t, v1.LabelManagedByValue, applied.Labels[v1.LabelManagedBy])
	assert.Equal(t, cluster.Spec.Version, applied.Labels[v1.NeutreeServingVersionLabel])

	lastApplied := &corev1.ConfigMap{}
	require.NoError(t, fakeClient.Get(ctx, client.ObjectKey{
		Name:      "neutree-cluster-hami-config",
		Namespace: "neutree-system",
	}, lastApplied))
	manifest, err := base64.StdEncoding.DecodeString(lastApplied.Data["last-applied-config"])
	require.NoError(t, err)
	assert.Contains(t, string(manifest), ownerResourceName)

	require.NoError(t, component.ApplyResources(ctx, defaultNodeScopePlan()))

	err = fakeClient.Get(ctx, client.ObjectKey{
		Name:      ownerResourceName,
		Namespace: "neutree-system",
	}, &corev1.ConfigMap{})
	assert.True(t, apierrors.IsNotFound(err))
	require.NoError(t, fakeClient.Get(ctx, client.ObjectKey{
		Name:      "neutree-cluster-hami-config",
		Namespace: "neutree-system",
	}, lastApplied))
	manifest, err = base64.StdEncoding.DecodeString(lastApplied.Data["last-applied-config"])
	require.NoError(t, err)
	assert.NotContains(t, string(manifest), ownerResourceName)
}

func TestHAMiComponentResourcesRejectInvalidOwnerDevicePluginTemplate(t *testing.T) {
	component := NewHAMiComponent(newTestCluster(), "neutree-system", "registry.example.com/neutree/",
		"image-pull-secret", v1.KubernetesClusterConfig{}, newHAMiFakeClient(t))
	plan := defaultNodeScopePlan()
	plan.DevicePluginTemplate = &accelerator.DevicePluginTemplate{Manifest: `
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Namespace }
`}

	_, err := component.renderResources(plan)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse template")
}

func TestHAMiComponentDevicePluginTemplateContextProvidesImagePrefix(t *testing.T) {
	tests := []struct {
		name        string
		imagePrefix string
		want        string
	}{
		{
			name:        "private registry is normalized",
			imagePrefix: "  registry.example.com/neutree/  ",
			want:        "registry.example.com/neutree",
		},
		{
			name:        "Docker Hub prefix is omitted",
			imagePrefix: "docker.io/neutree-ai/",
			want:        "",
		},
		{
			name:        "empty prefix is omitted",
			imagePrefix: "",
			want:        "",
		},
		{
			name:        "whitespace prefix is omitted",
			imagePrefix: "   ",
			want:        "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			component := NewHAMiComponent(newTestCluster(), "neutree-system", tt.imagePrefix,
				"image-pull-secret", v1.KubernetesClusterConfig{}, newHAMiFakeClient(t))
			plan := defaultNodeScopePlan()
			plan.DevicePluginTemplate = &accelerator.DevicePluginTemplate{Manifest: `
apiVersion: v1
kind: ConfigMap
metadata:
  name: owner-device-plugin
data:
  image-prefix: {{ .ImagePrefix | quote }}
`}

			resources, err := component.renderResources(plan)

			require.NoError(t, err)
			configMap := findObject(t, resources.Items, "ConfigMap", "owner-device-plugin")
			imagePrefix, found, err := unstructured.NestedString(configMap.Object, "data", "image-prefix")
			require.NoError(t, err)
			require.True(t, found)
			assert.Equal(t, tt.want, imagePrefix)
		})
	}
}

func TestHAMiComponentResourcesUseHAMiEntrypoints(t *testing.T) {
	component := NewHAMiComponent(newTestCluster(), "neutree-system", "registry.example.com/neutree",
		"image-pull-secret", v1.KubernetesClusterConfig{}, newHAMiFakeClient(t))

	objs, err := component.renderResources(nvidiaDevicePluginNodeScopePlan())
	require.NoError(t, err)

	scheduler := findContainer(t, objs.Items, "Deployment", SchedulerName, "vgpu-scheduler-extender")
	assert.Contains(t, stringSlice(scheduler["command"]), "scheduler")
	assert.Contains(t, stringSlice(scheduler["command"]), "--http_bind=0.0.0.0:443")
	assert.Contains(t, stringSlice(scheduler["command"]), "--device-config-file=/device-config.yaml")

	kubeScheduler := findContainer(t, objs.Items, "Deployment", SchedulerName, "kube-scheduler")
	assert.Equal(t, "registry.example.com/neutree/kube-scheduler:"+DefaultKubeSchedulerVersion(), kubeScheduler["image"])
	assert.Contains(t, stringSlice(kubeScheduler["command"]), "--config=/config/config.yaml")

	devicePlugin := findContainer(t, objs.Items, "DaemonSet", DevicePluginDaemonSetName, "device-plugin")
	assert.Contains(t, stringSlice(devicePlugin["command"]), "nvidia-device-plugin")
	assert.Contains(t, stringSlice(devicePlugin["command"]), "--config-file=/device-config.yaml")

	monitor := findContainer(t, objs.Items, "DaemonSet", DevicePluginDaemonSetName, "vgpu-monitor")
	assert.Contains(t, stringSlice(monitor["command"]), "vGPUmonitor")
}

func TestHAMiComponentRewritesImagesByRegistry(t *testing.T) {
	tests := []struct {
		name               string
		imagePrefix        string
		kubeSchedulerImage string
		hamiImage          string
	}{
		{
			name:               "docker hub preserves explicit upstream registries",
			imagePrefix:        "docker.io/neutree-ai",
			kubeSchedulerImage: "registry.k8s.io/kube-scheduler:" + DefaultKubeSchedulerVersion(),
			hamiImage:          "docker.io/projecthami/hami:" + Version,
		},
		{
			name:               "private registry rewrites all images",
			imagePrefix:        "registry.example.com/neutree-ai",
			kubeSchedulerImage: "registry.example.com/neutree-ai/kube-scheduler:" + DefaultKubeSchedulerVersion(),
			hamiImage:          "registry.example.com/neutree-ai/projecthami/hami:" + Version,
		},
		{
			name:               "docker hub without project preserves source registries",
			imagePrefix:        "docker.io",
			kubeSchedulerImage: "registry.k8s.io/kube-scheduler:" + DefaultKubeSchedulerVersion(),
			hamiImage:          "docker.io/projecthami/hami:" + Version,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			component := NewHAMiComponent(newTestCluster(), "neutree-system", tt.imagePrefix,
				"image-pull-secret", v1.KubernetesClusterConfig{}, newHAMiFakeClient(t))

			objs, err := component.renderResources(nvidiaDevicePluginNodeScopePlan())
			require.NoError(t, err)

			kubeScheduler := findContainer(t, objs.Items, "Deployment", SchedulerName, "kube-scheduler")
			assert.Equal(t, tt.kubeSchedulerImage, kubeScheduler["image"])

			extender := findContainer(t, objs.Items, "Deployment", SchedulerName, "vgpu-scheduler-extender")
			assert.Equal(t, tt.hamiImage, extender["image"])

			devicePlugin := findContainer(t, objs.Items, "DaemonSet", DevicePluginDaemonSetName, "device-plugin")
			assert.Equal(t, tt.hamiImage, devicePlugin["image"])

			monitor := findContainer(t, objs.Items, "DaemonSet", DevicePluginDaemonSetName, "vgpu-monitor")
			assert.Equal(t, tt.hamiImage, monitor["image"])
		})
	}
}

func TestHAMiComponentSchedulerUpdateStrategyAndAffinity(t *testing.T) {
	component := NewHAMiComponent(newTestCluster(), "neutree-system", "registry.example.com/neutree",
		"image-pull-secret", v1.KubernetesClusterConfig{}, newHAMiFakeClient(t))

	objs, err := component.renderResources(nvidiaDevicePluginNodeScopePlan())
	require.NoError(t, err)

	deployment := findObject(t, objs.Items, "Deployment", SchedulerName)
	require.NotNil(t, deployment)

	// Rollout must bring up the new scheduler before tearing down the old one so
	// a single-node cluster never loses its only scheduler mid-update.
	strategy, found, err := unstructured.NestedMap(deployment.Object, "spec", "strategy")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "RollingUpdate", strategy["type"])

	rollingUpdate, ok := strategy["rollingUpdate"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, int64(0), rollingUpdate["maxUnavailable"])
	assert.Equal(t, int64(1), rollingUpdate["maxSurge"])

	// Scheduler anti-affinity is a soft preference, not a hard requirement, so a
	// single-node cluster can still schedule the replacement pod during rollout.
	affinity, found, err := unstructured.NestedMap(deployment.Object, "spec", "template", "spec", "affinity")
	require.NoError(t, err)
	require.True(t, found)

	antiAffinity, ok := affinity["podAntiAffinity"].(map[string]interface{})
	require.True(t, ok)
	preferred, ok := antiAffinity["preferredDuringSchedulingIgnoredDuringExecution"].([]interface{})
	require.True(t, ok)
	require.Len(t, preferred, 1)
	_, hasRequired := antiAffinity["requiredDuringSchedulingIgnoredDuringExecution"]
	assert.False(t, hasRequired, "scheduler anti-affinity must not be hard-required")
}

func TestHAMiComponentWebhookFailurePolicyFailWithoutNamespaceOverride(t *testing.T) {
	const clusterNamespace = "neutree-system"

	component := NewHAMiComponent(newTestCluster(), clusterNamespace, "registry.example.com/neutree",
		"image-pull-secret", v1.KubernetesClusterConfig{}, newHAMiFakeClient(t))
	protectedValues := component.protectedChartValues()
	_, found, err := unstructured.NestedMap(protectedValues, "scheduler", "admissionWebhook", "namespaceSelector")
	require.NoError(t, err)
	assert.False(t, found, "Neutree must not override the chart namespaceSelector")

	objs, err := component.renderResources(nvidiaDevicePluginNodeScopePlan())
	require.NoError(t, err)

	webhook := findObject(t, objs.Items, "MutatingWebhookConfiguration", WebhookName)
	require.NotNil(t, webhook)

	webhooks, found, err := unstructured.NestedSlice(webhook.Object, "webhooks")
	require.NoError(t, err)
	require.True(t, found)
	require.NotEmpty(t, webhooks)

	first, ok := webhooks[0].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "Fail", first["failurePolicy"])

	// The HAMi chart always emits its default namespaceSelector. Neutree must
	// not replace it with an owning-namespace-only selector.
	namespaceSelector, found, err := unstructured.NestedMap(first, "namespaceSelector")
	require.NoError(t, err)
	require.True(t, found)

	expressions, found, err := unstructured.NestedSlice(namespaceSelector, "matchExpressions")
	require.NoError(t, err)
	require.True(t, found)

	defaultIgnoreSelectorFound := false
	for _, expression := range expressions {
		match, ok := expression.(map[string]interface{})
		require.True(t, ok)

		if match["key"] == "hami.io/webhook" && match["operator"] == "NotIn" {
			values, ok := match["values"].([]interface{})
			require.True(t, ok)
			assert.Equal(t, []interface{}{"ignore"}, values)
			defaultIgnoreSelectorFound = true
			continue
		}

		if match["key"] != "kubernetes.io/metadata.name" || match["operator"] != "In" {
			continue
		}

		values, ok := match["values"].([]interface{})
		require.True(t, ok)
		assert.NotEqual(t, []interface{}{clusterNamespace}, values,
			"webhook must not be restricted to the owning cluster namespace")
	}

	assert.True(t, defaultIgnoreSelectorFound,
		"webhook must preserve the chart default selector for hami.io/webhook=ignore")
}

func TestHAMiComponentDoesNotAllowAdmissionWebhookToBeDisabled(t *testing.T) {
	cluster := newTestCluster()
	cluster.Spec.AcceleratorVirtualization.ConfigPatch = map[string]interface{}{
		"scheduler": map[string]interface{}{
			"admissionWebhook": map[string]interface{}{"enabled": false},
		},
	}

	component := NewHAMiComponent(cluster, "neutree-system", "registry.example.com/neutree",
		"image-pull-secret", v1.KubernetesClusterConfig{}, newHAMiFakeClient(t))

	values := component.buildChartValues(nvidiaDevicePluginNodeScopePlan())
	assert.Equal(t, true, nestedMap(t, values, "scheduler", "admissionWebhook")["enabled"])

	objs, err := component.renderResources(nvidiaDevicePluginNodeScopePlan())
	require.NoError(t, err)
	assertHasObject(t, objs.Items, "MutatingWebhookConfiguration", WebhookName)
}

func TestHAMiComponentIgnoresAdmissionWebhookNamespaceSelectorOverride(t *testing.T) {
	cluster := newTestCluster()
	cluster.Spec.AcceleratorVirtualization.ConfigPatch = map[string]interface{}{
		"scheduler": map[string]interface{}{
			"admissionWebhook": map[string]interface{}{
				"namespaceSelector": map[string]interface{}{
					"matchExpressions": []interface{}{
						map[string]interface{}{
							"key":      "kubernetes.io/metadata.name",
							"operator": "In",
							"values":   []interface{}{"neutree-system"},
						},
					},
				},
			},
		},
	}

	component := NewHAMiComponent(cluster, "neutree-system", "registry.example.com/neutree",
		"image-pull-secret", v1.KubernetesClusterConfig{}, newHAMiFakeClient(t))

	values := component.buildChartValues(nvidiaDevicePluginNodeScopePlan())
	_, found, err := unstructured.NestedMap(values, "scheduler", "admissionWebhook", "namespaceSelector")
	require.NoError(t, err)
	assert.False(t, found, "Neutree must ignore custom namespaceSelector overrides")

	objs, err := component.renderResources(nvidiaDevicePluginNodeScopePlan())
	require.NoError(t, err)

	webhook := findObject(t, objs.Items, "MutatingWebhookConfiguration", WebhookName)
	require.NotNil(t, webhook)

	webhooks, found, err := unstructured.NestedSlice(webhook.Object, "webhooks")
	require.NoError(t, err)
	require.True(t, found)
	require.NotEmpty(t, webhooks)

	for _, entry := range webhooks {
		webhookEntry, ok := entry.(map[string]interface{})
		require.True(t, ok)

		namespaceSelector, found, err := unstructured.NestedMap(webhookEntry, "namespaceSelector")
		require.NoError(t, err)
		require.True(t, found)

		expressions, found, err := unstructured.NestedSlice(namespaceSelector, "matchExpressions")
		require.NoError(t, err)
		require.True(t, found)

		for _, expression := range expressions {
			match, ok := expression.(map[string]interface{})
			require.True(t, ok)

			if match["key"] != "kubernetes.io/metadata.name" || match["operator"] != "In" {
				continue
			}

			values, ok := match["values"].([]interface{})
			require.True(t, ok)
			assert.NotEqual(t, []interface{}{"neutree-system"}, values,
				"webhook must not accept a custom owning-namespace selector")
		}
	}
}

func TestHAMiComponentDeviceConfigChecksumRotation(t *testing.T) {
	const deviceConfigChecksumAnnotation = "checksum/hami-scheduler-device-config"

	type deviceChecksums struct {
		scheduler    string
		devicePlugin string
	}

	renderChecksums := func(deviceConfigContent interface{}) deviceChecksums {
		component := NewHAMiComponent(newTestCluster(), "neutree-system", "registry.example.com/neutree",
			"image-pull-secret", v1.KubernetesClusterConfig{}, newHAMiFakeClient(t))

		plan := nvidiaDevicePluginNodeScopePlan()
		if deviceConfigContent != nil {
			if plan.ConfigPatch == nil {
				plan.ConfigPatch = map[string]interface{}{}
			}

			plan.ConfigPatch["device-config"] = map[string]interface{}{
				"content": deviceConfigContent,
			}
		}

		objs, err := component.renderResources(plan)
		require.NoError(t, err)

		deployment := findObject(t, objs.Items, "Deployment", SchedulerName)
		require.NotNil(t, deployment)

		schedulerChecksum, found, err := unstructured.NestedString(
			deployment.Object, "spec", "template", "metadata", "annotations", deviceConfigChecksumAnnotation)
		require.NoError(t, err)
		require.True(t, found, "scheduler Deployment must carry the device-config checksum annotation")

		daemonSet := findObject(t, objs.Items, "DaemonSet", DevicePluginDaemonSetName)
		require.NotNil(t, daemonSet)

		devicePluginChecksum, found, err := unstructured.NestedString(
			daemonSet.Object, "spec", "template", "metadata", "annotations", deviceConfigChecksumAnnotation)
		require.NoError(t, err)
		require.True(t, found, "device plugin DaemonSet must carry the device-config checksum annotation")

		return deviceChecksums{
			scheduler:    schedulerChecksum,
			devicePlugin: devicePluginChecksum,
		}
	}

	baseline := renderChecksums(nil)
	changed := renderChecksums("nvidia:\n  resourceCountName: nvidia.com/gpu\n  resourceMemoryName: nvidia.com/gpumem\n")

	// Both the scheduler Deployment and the device plugin DaemonSet read the
	// same hami-scheduler-device ConfigMap. A device-config content change must
	// rotate the checksum on both so scheduler and device plugin roll out
	// together and never run with divergent device configs.
	assert.NotEqual(t, baseline.scheduler, changed.scheduler,
		"device-config content change must rotate the scheduler checksum to trigger a scheduler rollout")
	assert.NotEqual(t, baseline.devicePlugin, changed.devicePlugin,
		"device-config content change must rotate the device plugin checksum to trigger a device plugin rollout")
}

func TestHAMiComponentDevicePluginNodeSelectorUsesVirtualizationLabelOnly(t *testing.T) {
	component := NewHAMiComponent(newTestCluster(), "neutree-system", "registry.example.com/neutree",
		"image-pull-secret", v1.KubernetesClusterConfig{}, newHAMiFakeClient(t))

	objs, err := component.renderResources(nvidiaDevicePluginNodeScopePlan())
	require.NoError(t, err)

	devicePlugin := findObject(t, objs.Items, "DaemonSet", DevicePluginDaemonSetName)
	nodeSelector, found, err := unstructured.NestedStringMap(devicePlugin.Object,
		"spec", "template", "spec", "nodeSelector")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, map[string]string{
		plugin.NvidiaGPUVirtualizationLabelKey: "true",
	}, nodeSelector)
}

func TestHAMiComponentKubeSchedulerVersionUsesDetectedClusterMinor(t *testing.T) {
	originalGetKubernetesServerVersion := getKubernetesServerVersion
	getKubernetesServerVersion = func(*v1.Cluster) (*kubeversion.Info, error) {
		return &kubeversion.Info{
			Major:      "1",
			Minor:      "30+",
			GitVersion: "v1.30.9",
		}, nil
	}
	t.Cleanup(func() {
		getKubernetesServerVersion = originalGetKubernetesServerVersion
	})
	component := NewHAMiComponent(newTestCluster(), "neutree-system", "registry.example.com/neutree",
		"image-pull-secret", v1.KubernetesClusterConfig{}, newHAMiFakeClient(t))

	values := component.buildChartValues(NodeScopePlan{})

	assert.Equal(t, KubeSchedulerVersionsByMinor["1.30"],
		nestedMap(t, values, "scheduler", "kubeScheduler", "image")["tag"])
}

func TestHAMiComponentKubeSchedulerVersionUsesDetectedVersionWhenMinorIsUnmapped(t *testing.T) {
	originalGetKubernetesServerVersion := getKubernetesServerVersion
	getKubernetesServerVersion = func(*v1.Cluster) (*kubeversion.Info, error) {
		return &kubeversion.Info{
			Major:      "1",
			Minor:      "40",
			GitVersion: "v1.40.0",
		}, nil
	}
	t.Cleanup(func() {
		getKubernetesServerVersion = originalGetKubernetesServerVersion
	})
	component := NewHAMiComponent(newTestCluster(), "neutree-system", "registry.example.com/neutree",
		"image-pull-secret", v1.KubernetesClusterConfig{}, newHAMiFakeClient(t))

	values := component.buildChartValues(NodeScopePlan{})

	assert.Equal(t, "v1.40.0",
		nestedMap(t, values, "scheduler", "kubeScheduler", "image")["tag"])
}

func TestHAMiPreflightRejectsUnsupportedClusterVersion(t *testing.T) {
	cluster := newTestCluster()
	cluster.Spec.Version = "v1.0.9"
	component := NewHAMiComponent(cluster, "neutree-system", "registry.example.com/neutree",
		"image-pull-secret", v1.KubernetesClusterConfig{}, newHAMiFakeClient(t))

	err := component.Preflight(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires cluster version >= v1.1.0")
}

func TestHAMiComponentIgnoresLegacyDevicePluginConfigPatchDuringPreflight(t *testing.T) {
	cluster := newTestCluster()
	cluster.Spec.AcceleratorVirtualization.ConfigPatch = map[string]interface{}{
		"devicePlugin": map[string]interface{}{
			"migStrategy": "mixed",
		},
	}
	component := NewHAMiComponent(cluster, "neutree-system", "registry.example.com/neutree",
		"image-pull-secret", v1.KubernetesClusterConfig{}, newHAMiFakeClient(t))

	err := component.Preflight(context.Background())

	require.NoError(t, err)
}

func TestHAMiComponentLegacyDevicePluginConfigPatchCannotOverrideOwner(t *testing.T) {
	cluster := newTestCluster()
	cluster.Spec.AcceleratorVirtualization.ConfigPatch = map[string]interface{}{
		"devicePlugin": map[string]interface{}{
			"enabled":          false,
			"nvidiaDriverRoot": "/legacy-driver",
			"nvidiaNodeSelector": map[string]interface{}{
				"example.com/legacy": "true",
			},
		},
		"scheduler": map[string]interface{}{
			"defaultSchedulerPolicy": map[string]interface{}{
				"nodeSchedulerPolicy": "spread",
			},
		},
		"global": map[string]interface{}{
			"legacySetting": "preserved",
		},
	}
	component := NewHAMiComponent(cluster, "neutree-system", "registry.example.com/neutree",
		"image-pull-secret", v1.KubernetesClusterConfig{}, newHAMiFakeClient(t))
	plan := defaultNodeScopePlan()
	plan.ConfigPatch = map[string]interface{}{
		"devicePlugin": map[string]interface{}{
			"enabled":          true,
			"nvidiaDriverRoot": "/plugin-driver",
			"nvidiaNodeSelector": map[string]interface{}{
				"example.com/owner": "true",
			},
		},
	}

	values := component.buildChartValues(plan)

	devicePlugin := nestedMap(t, values, "devicePlugin")
	assert.Equal(t, true, devicePlugin["enabled"])
	assert.Equal(t, "/plugin-driver", devicePlugin["nvidiaDriverRoot"])
	assert.Equal(t, map[string]interface{}{
		"example.com/owner": "true",
	}, devicePlugin["nvidiaNodeSelector"])
	assert.Equal(t, "spread",
		nestedMap(t, values, "scheduler", "defaultSchedulerPolicy")["nodeSchedulerPolicy"])
	assert.Equal(t, "preserved", nestedMap(t, values, "global")["legacySetting"])
	assert.Contains(t, cluster.Spec.AcceleratorVirtualization.ConfigPatch, "devicePlugin")
}

func TestHAMiComponentDefaultValuesDoNotOwnNVIDIADevicePluginPolicy(t *testing.T) {
	component := NewHAMiComponent(newTestCluster(), "neutree-system", "registry.example.com/neutree",
		"image-pull-secret", v1.KubernetesClusterConfig{}, newHAMiFakeClient(t))

	values := component.buildChartValues(NodeScopePlan{})
	devicePlugin := nestedMap(t, values, "devicePlugin")

	assert.NotContains(t, devicePlugin, "migStrategy")
	assert.NotContains(t, devicePlugin, "deviceSplitCount")
}

func TestHAMiComponentUsesGPUTopologyAwareSchedulerPolicy(t *testing.T) {
	component := NewHAMiComponent(newTestCluster(), "neutree-system", "registry.example.com/neutree",
		"image-pull-secret", v1.KubernetesClusterConfig{}, newHAMiFakeClient(t))

	values := component.buildChartValues(NodeScopePlan{})

	defaultSchedulerPolicy := nestedMap(t, values, "scheduler", "defaultSchedulerPolicy")
	assert.Equal(t, plugin.NvidiaGPUTopologyAwarePolicy, defaultSchedulerPolicy["gpuSchedulerPolicy"])
}

func TestHAMiComponentStatusReadyWhenSharedResourcesAndNodeScopeAreReady(t *testing.T) {
	tlsSecret := newHAMiTLSSecret(t, "neutree-system")
	fakeClient := newHAMiFakeClient(t,
		newHAMiReadyDeployment("neutree-system"),
		newHAMiDeviceConfig("neutree-system"),
		tlsSecret,
		newHAMiWebhook(tlsSecret.Data["ca.crt"]),
		newHAMiNode("gpu-1", map[string]string{
			plugin.NvidiaGPUVirtualizationLabelKey: "true",
			"nvidia.com/gpu.present":               "true",
		}),
		newHAMiNode("gpu-2", map[string]string{
			plugin.NvidiaGPUVirtualizationLabelKey: "true",
			"nvidia.com/gpu.present":               "true",
		}),
	)
	component := NewHAMiComponent(newTestCluster(), "neutree-system", "registry.example.com/neutree/",
		"image-pull-secret", v1.KubernetesClusterConfig{}, fakeClient, newTestPluginProvider("gpu-1", "gpu-2"))

	status, err := component.CheckResourcesStatus(context.Background())

	require.NoError(t, err)
	assert.True(t, status.Ready)
	assert.True(t, status.DeviceConfigReady)
	assert.Equal(t, 2, status.ReadyNodes)
	assert.Equal(t, 2, status.DesiredNodes)
}

func TestHAMiComponentStatusReadyWithoutVendorDaemonSetOrMonitor(t *testing.T) {
	cluster := newTestCluster()
	tlsSecret := newHAMiTLSSecret(t, "neutree-system")
	component := NewHAMiComponent(cluster, "neutree-system", "registry.example.com/neutree/",
		"image-pull-secret", v1.KubernetesClusterConfig{}, newHAMiFakeClient(t,
			newHAMiReadyDeployment("neutree-system"),
			newHAMiDeviceConfig("neutree-system"),
			tlsSecret,
			newHAMiWebhook(tlsSecret.Data["ca.crt"]),
			newHAMiNode("gpu-1", map[string]string{
				plugin.NvidiaGPUVirtualizationLabelKey: "true",
				"nvidia.com/gpu.present":               "true",
			}),
		), newTestPluginProvider("gpu-1"))

	err := component.UpdateStatus(context.Background())

	require.NoError(t, err)
	require.NotNil(t, cluster.Status.ComponentStatus[v1.ComponentStatusAcceleratorVirtualizationKey])
	assert.Equal(t, v1.ComponentPhaseReady, cluster.Status.ComponentStatus[v1.ComponentStatusAcceleratorVirtualizationKey].Phase)
	assert.Equal(t, "Ready", cluster.Status.ComponentStatus[v1.ComponentStatusAcceleratorVirtualizationKey].Reason)
}

func TestHAMiComponentStatusIsNotReadyWhenSharedDeviceConfigIsMissing(t *testing.T) {
	tlsSecret := newHAMiTLSSecret(t, "neutree-system")
	component := NewHAMiComponent(newTestCluster(), "neutree-system", "registry.example.com/neutree/",
		"image-pull-secret", v1.KubernetesClusterConfig{}, newHAMiFakeClient(t,
			newHAMiReadyDeployment("neutree-system"),
			tlsSecret,
			newHAMiWebhook(tlsSecret.Data["ca.crt"]),
			newHAMiNode("gpu-1", map[string]string{
				plugin.NvidiaGPUVirtualizationLabelKey: "true",
				"nvidia.com/gpu.present":               "true",
			}),
		), newTestPluginProvider("gpu-1"))

	status, err := component.CheckResourcesStatus(context.Background())

	require.NoError(t, err)
	assert.False(t, status.Ready)
	assert.Equal(t, "DeviceConfigNotReady", status.Reason)
}

func TestHAMiStatusErrorMessageIncludesReason(t *testing.T) {
	err := hamiStatusError(&HAMiStatus{
		Reason:  "DaemonSetNotReady",
		Message: "daemonset hami-device-plugin ready 0/1",
	})

	require.Error(t, err)
	assert.Equal(t,
		"accelerator virtualization component is not ready: DaemonSetNotReady daemonset hami-device-plugin ready 0/1",
		err.Error(),
	)
}

func TestHAMiStatusErrorMessageOmitsEmptyDetails(t *testing.T) {
	err := hamiStatusError(&HAMiStatus{})

	require.Error(t, err)
	assert.Equal(t, "accelerator virtualization component is not ready", err.Error())
}

func TestHAMiComponentNodeScopeUsesPluginVirtualizationConfig(t *testing.T) {
	fakeClient := newHAMiFakeClient(t,
		newHAMiNode("plugin-candidate", map[string]string{}),
		&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "component-local-gpu",
			},
			Status: corev1.NodeStatus{
				Allocatable: corev1.ResourceList{
					plugin.NvidiaGPUKubernetesResource: resource.MustParse("1"),
				},
			},
		},
	)
	nvidiaPlugin := fakeAcceleratorPlugin{
		acceleratorType: string(v1.AcceleratorTypeNVIDIAGPU),
		config: &accelerator.VirtualizationConfig{
			Supported:      true,
			CandidateNodes: []string{"plugin-candidate"},
			NodeScopeLabel: accelerator.VirtualizationNodeScopeLabel{
				Key:           plugin.NvidiaGPUVirtualizationLabelKey,
				EnabledValue:  "true",
				DisabledValue: "false",
			},
			ConfigPatch: map[string]interface{}{
				"devicePlugin": map[string]interface{}{
					"nvidiaDriverRoot": plugin.NvidiaGPUOperatorDriverRoot,
				},
			},
		},
	}
	pluginProvider := fakePluginProvider{
		plugins: map[string]plugin.AcceleratorPlugin{
			string(v1.AcceleratorTypeNVIDIAGPU): nvidiaPlugin,
			string(v1.AcceleratorTypeAMDGPU):    &plugin.AMDGPUAcceleratorPlugin{},
		},
		supportedPlugins: []string{
			string(v1.AcceleratorTypeAMDGPU),
			string(v1.AcceleratorTypeNVIDIAGPU),
		},
	}
	component := NewHAMiComponent(newTestCluster(), "neutree-system", "registry.example.com/neutree/",
		"image-pull-secret", v1.KubernetesClusterConfig{}, fakeClient, pluginProvider)

	plan, err := component.ReconcileNodeScope(context.Background())

	require.NoError(t, err)
	assert.Equal(t, []string{"plugin-candidate"}, plan.PatchedNodes)
	assert.Equal(t, plugin.NvidiaGPUOperatorDriverRoot,
		nestedMap(t, component.buildChartValues(plan), "devicePlugin")["nvidiaDriverRoot"])

	patched := &corev1.Node{}
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKey{Name: "plugin-candidate"}, patched))
	assert.Equal(t, "true", patched.Labels[plugin.NvidiaGPUVirtualizationLabelKey])

	unselected := &corev1.Node{}
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKey{Name: "component-local-gpu"}, unselected))
	assert.NotContains(t, unselected.Labels, plugin.NvidiaGPUVirtualizationLabelKey)
}

func TestHAMiPreflightRejectsProtectedConfigPatch(t *testing.T) {
	cluster := newTestCluster()
	cluster.Spec.AcceleratorVirtualization.ConfigPatch = map[string]interface{}{
		"dra": map[string]interface{}{
			"enabled": true,
		},
	}
	component := NewHAMiComponent(cluster, "neutree-system", "registry.example.com/neutree/",
		"image-pull-secret", v1.KubernetesClusterConfig{}, newHAMiFakeClient(t))

	err := component.Preflight(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), `unsupported accelerator_virtualization.config_patch key "dra"`)
}

func TestHAMiPreflightRejectsUnmanagedWebhook(t *testing.T) {
	component := NewHAMiComponent(newTestCluster(), "neutree-system", "registry.example.com/neutree/",
		"image-pull-secret", v1.KubernetesClusterConfig{}, newHAMiFakeClient(t, newHAMiWebhook(nil)))

	err := component.Preflight(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmanaged HAMi webhook")
}

func TestHAMiPreflightAllowsUnmanagedDaemonSetWhenNVIDIAPluginIsDisabled(t *testing.T) {
	component := NewHAMiComponent(newTestCluster(), "neutree-system", "registry.example.com/neutree/",
		"image-pull-secret", v1.KubernetesClusterConfig{}, newHAMiFakeClient(t, &appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      DevicePluginDaemonSetName,
				Namespace: "neutree-system",
			},
		}))

	err := component.Preflight(context.Background())

	require.NoError(t, err)
}

func TestHAMiPreflightAllowsUnmanagedConfigMap(t *testing.T) {
	component := NewHAMiComponent(newTestCluster(), "neutree-system", "registry.example.com/neutree/",
		"image-pull-secret", v1.KubernetesClusterConfig{}, newHAMiFakeClient(t, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      SchedulerName + "-device",
				Namespace: "neutree-system",
			},
		}))

	err := component.Preflight(context.Background())

	require.NoError(t, err)
}

func TestHAMiPreflightAllowsUnmanagedClusterRoleBinding(t *testing.T) {
	component := NewHAMiComponent(newTestCluster(), "neutree-system", "registry.example.com/neutree/",
		"image-pull-secret", v1.KubernetesClusterConfig{}, newHAMiFakeClient(t, &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: SchedulerName + "-kube",
			},
		}))

	err := component.Preflight(context.Background())

	require.NoError(t, err)
}

func TestHAMiPreflightAllowsUnmanagedRuntimeClassWhenNVIDIAPluginIsDisabled(t *testing.T) {
	cluster := newTestCluster()
	cluster.Spec.AcceleratorVirtualization.ConfigPatch = map[string]interface{}{
		"devicePlugin": map[string]interface{}{
			"createRuntimeClass": true,
			"runtimeClassName":   "nvidia",
		},
	}
	component := NewHAMiComponent(cluster, "neutree-system", "registry.example.com/neutree/",
		"image-pull-secret", v1.KubernetesClusterConfig{}, newHAMiFakeClient(t, &nodev1.RuntimeClass{
			ObjectMeta: metav1.ObjectMeta{
				Name: "nvidia",
			},
		}))

	err := component.Preflight(context.Background())

	require.NoError(t, err)
}

func TestHAMiServingCertificateRenewalWindow(t *testing.T) {
	now := time.Now()
	fresh := newHAMiTLSSecret(t, "neutree-system")
	expiringBundle, err := generateTLSBundle("neutree-system", now.AddDate(-1, 0, 0).Add((ServingCertificateRenewDays-1)*24*time.Hour))
	require.NoError(t, err)
	expiring := &corev1.Secret{
		Data: map[string][]byte{
			corev1.TLSCertKey:       expiringBundle.CertPEM,
			corev1.TLSPrivateKeyKey: expiringBundle.KeyPEM,
			"ca.crt":                expiringBundle.CAPEM,
		},
	}

	assert.False(t, servingCertificateNeedsRenewal(fresh, now))
	assert.True(t, servingCertificateNeedsRenewal(expiring, now))
}

func TestHAMiEnsureTLSReportsChangeWhenCertificateNeedsRenewal(t *testing.T) {
	expiringBundle, err := generateTLSBundle("neutree-system",
		time.Now().AddDate(-1, 0, 0).Add((ServingCertificateRenewDays-1)*24*time.Hour))
	require.NoError(t, err)
	expiring := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      TLSSecretName,
			Namespace: "neutree-system",
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			corev1.TLSCertKey:       expiringBundle.CertPEM,
			corev1.TLSPrivateKeyKey: expiringBundle.KeyPEM,
			"ca.crt":                expiringBundle.CAPEM,
		},
	}
	fakeClient := newHAMiFakeClient(t, expiring)
	component := NewHAMiComponent(newTestCluster(), "neutree-system", "registry.example.com/neutree/",
		"image-pull-secret", v1.KubernetesClusterConfig{}, fakeClient)

	changed, err := component.EnsureTLS(context.Background())

	require.NoError(t, err)
	assert.True(t, changed)
}

func TestHAMiEnsureTLSReportsNoChangeWhenCertificateIsFresh(t *testing.T) {
	tlsSecret := newHAMiTLSSecret(t, "neutree-system")
	fakeClient := newHAMiFakeClient(t, tlsSecret)
	component := NewHAMiComponent(newTestCluster(), "neutree-system", "registry.example.com/neutree/",
		"image-pull-secret", v1.KubernetesClusterConfig{}, fakeClient)

	changed, err := component.EnsureTLS(context.Background())

	require.NoError(t, err)
	assert.False(t, changed)
}

func TestHAMiRolloutSchedulerPatchesPodTemplateAnnotation(t *testing.T) {
	fakeClient := newHAMiFakeClient(t, newHAMiReadyDeployment("neutree-system"))
	component := NewHAMiComponent(newTestCluster(), "neutree-system", "registry.example.com/neutree/",
		"image-pull-secret", v1.KubernetesClusterConfig{}, fakeClient)

	err := component.rolloutScheduler(context.Background())

	require.NoError(t, err)
	deployment := &appsv1.Deployment{}
	require.NoError(t, fakeClient.Get(context.Background(),
		client.ObjectKey{Name: SchedulerName, Namespace: "neutree-system"}, deployment))
	assert.NotEmpty(t, deployment.Spec.Template.Annotations[schedulerTLSRolloutAnnotation])
}

func TestHAMiPatchWebhookCABundleWritesCA(t *testing.T) {
	tlsSecret := newHAMiTLSSecret(t, "neutree-system")
	fakeClient := newHAMiFakeClient(t, tlsSecret, newHAMiWebhook(nil))
	component := NewHAMiComponent(newTestCluster(), "neutree-system", "registry.example.com/neutree/",
		"image-pull-secret", v1.KubernetesClusterConfig{}, fakeClient)

	changed, err := component.PatchWebhookCABundle(context.Background())

	require.NoError(t, err)
	assert.True(t, changed)
	webhook := &admissionregistrationv1.MutatingWebhookConfiguration{}
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKey{Name: WebhookName}, webhook))
	require.Len(t, webhook.Webhooks, 1)
	assert.Equal(t, tlsSecret.Data["ca.crt"], webhook.Webhooks[0].ClientConfig.CABundle)
}

func TestHAMiPatchWebhookCABundleNoopWhenCAIsCurrent(t *testing.T) {
	tlsSecret := newHAMiTLSSecret(t, "neutree-system")
	fakeClient := newHAMiFakeClient(t, tlsSecret, newHAMiWebhook(tlsSecret.Data["ca.crt"]))
	component := NewHAMiComponent(newTestCluster(), "neutree-system", "registry.example.com/neutree/",
		"image-pull-secret", v1.KubernetesClusterConfig{}, fakeClient)

	changed, err := component.PatchWebhookCABundle(context.Background())

	require.NoError(t, err)
	assert.False(t, changed)
}

func TestHAMiDeleteRemovesTLSSecret(t *testing.T) {
	tlsSecret := newHAMiTLSSecret(t, "neutree-system")
	fakeClient := newHAMiFakeClient(t, tlsSecret)
	component := NewHAMiComponent(newTestCluster(), "neutree-system", "registry.example.com/neutree/",
		"image-pull-secret", v1.KubernetesClusterConfig{}, fakeClient, newTestPluginProvider())

	err := component.Delete()

	require.NoError(t, err)
	got := &corev1.Secret{}
	err = fakeClient.Get(context.Background(), client.ObjectKey{Name: TLSSecretName, Namespace: "neutree-system"}, got)
	assert.True(t, apierrors.IsNotFound(err))
}

func TestWriteAndClearVirtualizationStatus(t *testing.T) {
	cluster := &v1.Cluster{Status: &v1.ClusterStatus{}}
	h := &HAMiComponent{cluster: cluster}

	h.writeVirtualizationStatus(&HAMiStatus{
		VirtualizationMode: v1.AcceleratorVirtualizationModeCore,
		SupportedResources: []string{v1.AcceleratorVirtualizationMemoryMiBKey, v1.AcceleratorVirtualizationCorePercentKey},
	})
	require.NotNil(t, h.cluster.Status.AcceleratorVirtualization)
	assert.Equal(t, v1.AcceleratorVirtualizationModeCore, h.cluster.Status.AcceleratorVirtualization.Mode)
	assert.Equal(t, []string{v1.AcceleratorVirtualizationMemoryMiBKey, v1.AcceleratorVirtualizationCorePercentKey},
		h.cluster.Status.AcceleratorVirtualization.SupportedResources)

	h.clearStatus()
	assert.Nil(t, h.cluster.Status.AcceleratorVirtualization)
}

func TestWriteVirtualizationStatusSkipsEmptyMode(t *testing.T) {
	cluster := &v1.Cluster{Status: &v1.ClusterStatus{}}
	h := &HAMiComponent{cluster: cluster}

	h.writeVirtualizationStatus(&HAMiStatus{})

	assert.Nil(t, h.cluster.Status.AcceleratorVirtualization)
}

func TestHAMiDeleteRemovesComponentStatus(t *testing.T) {
	cluster := newTestCluster()
	cluster.Status = &v1.ClusterStatus{
		ComponentStatus: map[string]*v1.ComponentStatus{
			v1.ComponentStatusAcceleratorVirtualizationKey: {
				Phase: v1.ComponentPhaseReady,
			},
		},
	}
	component := NewHAMiComponent(cluster, "neutree-system", "registry.example.com/neutree/",
		"image-pull-secret", v1.KubernetesClusterConfig{}, newHAMiFakeClient(t), newTestPluginProvider())

	err := component.Delete()

	require.NoError(t, err)
	assert.NotContains(t, cluster.Status.ComponentStatus, v1.ComponentStatusAcceleratorVirtualizationKey)
}

func TestHAMiDeleteRemovesOwnedNodeScopeState(t *testing.T) {
	enabledNode := newHAMiNode("gpu-enabled", map[string]string{
		plugin.NvidiaGPUVirtualizationLabelKey: "true",
	})
	enabledNode.Annotations = map[string]string{
		"hami.io/node-nvidia-register":                     `[{"id":"GPU-enabled"}]`,
		"hami.io/node-nvidia-score":                        `[{"uuid":"GPU-enabled"}]`,
		"hami.io/node-handshake":                           "Reported",
		"hami.io/mutex.lock":                               "lock-values,default,pod-gpu",
		resourceparser.NeutreeAcceleratorDevicesAnnotation: `[{"uuid":"GPU-enabled"}]`,
	}
	disabledNode := newHAMiNode("gpu-disabled", map[string]string{
		plugin.NvidiaGPUVirtualizationLabelKey: "false",
	})
	disabledNode.Annotations = map[string]string{
		"hami.io/node-nvidia-register":                     `[{"id":"GPU-disabled"}]`,
		resourceparser.NeutreeAcceleratorDevicesAnnotation: `[{"uuid":"GPU-disabled"}]`,
	}
	unlabeledNode := newHAMiNode("gpu-unlabeled", map[string]string{})
	unlabeledNode.Annotations = map[string]string{
		"hami.io/node-nvidia-register":                     `[{"id":"GPU-unlabeled"}]`,
		resourceparser.NeutreeAcceleratorDevicesAnnotation: `[{"uuid":"GPU-unlabeled"}]`,
	}
	fakeClient := newHAMiFakeClient(t, enabledNode, disabledNode, unlabeledNode)
	cluster := newTestCluster()
	markHAMiOwned(cluster)
	component := NewHAMiComponent(cluster, "neutree-system", "registry.example.com/neutree/",
		"image-pull-secret", v1.KubernetesClusterConfig{}, fakeClient, newTestPluginProvider())

	err := component.Delete()

	require.NoError(t, err)

	gotEnabled := &corev1.Node{}
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKey{Name: "gpu-enabled"}, gotEnabled))
	assert.NotContains(t, gotEnabled.Labels, plugin.NvidiaGPUVirtualizationLabelKey)
	assertHAMiNodeAnnotationsRemoved(t, gotEnabled.Annotations)
	assert.Contains(t, gotEnabled.Annotations, resourceparser.NeutreeAcceleratorDevicesAnnotation)

	gotDisabled := &corev1.Node{}
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKey{Name: "gpu-disabled"}, gotDisabled))
	assert.Equal(t, "false", gotDisabled.Labels[plugin.NvidiaGPUVirtualizationLabelKey])
	assertHAMiNodeAnnotationsRemoved(t, gotDisabled.Annotations)
	assert.Contains(t, gotDisabled.Annotations, resourceparser.NeutreeAcceleratorDevicesAnnotation)

	gotUnlabeled := &corev1.Node{}
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKey{Name: "gpu-unlabeled"}, gotUnlabeled))
	assert.NotContains(t, gotUnlabeled.Labels, plugin.NvidiaGPUVirtualizationLabelKey)
	assertHAMiNodeAnnotationsRemoved(t, gotUnlabeled.Annotations)
	assert.Contains(t, gotUnlabeled.Annotations, resourceparser.NeutreeAcceleratorDevicesAnnotation)
}

func TestHAMiDeleteSkipsNodeScopeCleanupWhenClusterDoesNotOwnVirtualization(t *testing.T) {
	enabledNode := newHAMiNode("gpu-enabled", map[string]string{
		plugin.NvidiaGPUVirtualizationLabelKey: "true",
	})
	enabledNode.Annotations = map[string]string{
		"hami.io/node-nvidia-register":                     `[{"id":"GPU-owned-by-other"}]`,
		resourceparser.NeutreeAcceleratorDevicesAnnotation: `[{"uuid":"GPU-owned-by-other"}]`,
	}
	fakeClient := newHAMiFakeClient(t, enabledNode)
	cluster := newTestCluster()
	cluster.Spec.AcceleratorVirtualization = nil
	component := NewHAMiComponent(cluster, "neutree-system", "registry.example.com/neutree/",
		"image-pull-secret", v1.KubernetesClusterConfig{}, fakeClient, newTestPluginProvider())

	err := component.Delete()

	require.NoError(t, err)

	gotEnabled := &corev1.Node{}
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKey{Name: "gpu-enabled"}, gotEnabled))
	assert.Equal(t, "true", gotEnabled.Labels[plugin.NvidiaGPUVirtualizationLabelKey])
	assert.Contains(t, gotEnabled.Annotations, "hami.io/node-nvidia-register")
	assert.Contains(t, gotEnabled.Annotations, resourceparser.NeutreeAcceleratorDevicesAnnotation)
}

func TestHAMiDeleteRemovesNodeScopeWhenSpecStillEnablesVirtualization(t *testing.T) {
	enabledNode := newHAMiNode("gpu-enabled", map[string]string{
		plugin.NvidiaGPUVirtualizationLabelKey: "true",
	})
	fakeClient := newHAMiFakeClient(t, enabledNode)
	component := NewHAMiComponent(newTestCluster(), "neutree-system", "registry.example.com/neutree/",
		"image-pull-secret", v1.KubernetesClusterConfig{}, fakeClient, newTestPluginProvider())

	err := component.Delete()

	require.NoError(t, err)

	gotEnabled := &corev1.Node{}
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKey{Name: "gpu-enabled"}, gotEnabled))
	assert.NotContains(t, gotEnabled.Labels, plugin.NvidiaGPUVirtualizationLabelKey)
}

func TestHAMiDeleteUsesPluginNodeScopeLabel(t *testing.T) {
	const customLabelKey = "example.com/custom-vgpu-enabled"
	customLabel := accelerator.VirtualizationNodeScopeLabel{
		Key:           customLabelKey,
		EnabledValue:  "enabled",
		DisabledValue: "disabled",
	}
	customEnabledNode := newHAMiNode("custom-enabled", map[string]string{
		customLabelKey: "enabled",
	})
	customDisabledNode := newHAMiNode("custom-disabled", map[string]string{
		customLabelKey: "disabled",
	})
	defaultEnabledNode := newHAMiNode("default-enabled", map[string]string{
		plugin.NvidiaGPUVirtualizationLabelKey: "true",
	})
	fakeClient := newHAMiFakeClient(t, customEnabledNode, customDisabledNode, defaultEnabledNode)
	cluster := newTestCluster()
	markHAMiOwned(cluster)
	component := NewHAMiComponent(cluster, "neutree-system", "registry.example.com/neutree/",
		"image-pull-secret", v1.KubernetesClusterConfig{}, fakeClient,
		newTestPluginProviderWithNodeScopeLabel(customLabel))

	err := component.Delete()

	require.NoError(t, err)

	gotCustomEnabled := &corev1.Node{}
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKey{Name: "custom-enabled"}, gotCustomEnabled))
	assert.NotContains(t, gotCustomEnabled.Labels, customLabelKey)

	gotCustomDisabled := &corev1.Node{}
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKey{Name: "custom-disabled"}, gotCustomDisabled))
	assert.Equal(t, "disabled", gotCustomDisabled.Labels[customLabelKey])

	gotDefaultEnabled := &corev1.Node{}
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKey{Name: "default-enabled"}, gotDefaultEnabled))
	assert.Equal(t, "true", gotDefaultEnabled.Labels[plugin.NvidiaGPUVirtualizationLabelKey])
}

func assertHAMiNodeAnnotationsRemoved(t *testing.T, annotations map[string]string) {
	t.Helper()

	for _, key := range testHAMiNodeAnnotations {
		assert.NotContains(t, annotations, key)
	}
}

func assertHasObject(t *testing.T, items []unstructured.Unstructured, kind, name string) {
	t.Helper()

	for _, item := range items {
		if item.GetKind() == kind && item.GetName() == name {
			return
		}
	}

	t.Fatalf("expected rendered %s/%s", kind, name)
}

func assertNoObject(t *testing.T, items []unstructured.Unstructured, kind, name string) {
	t.Helper()

	for _, item := range items {
		if item.GetKind() == kind && item.GetName() == name {
			t.Fatalf("did not expect rendered %s/%s", kind, name)
		}
	}
}

func findObject(t *testing.T, items []unstructured.Unstructured, kind, name string) *unstructured.Unstructured {
	t.Helper()

	for i := range items {
		if items[i].GetKind() == kind && items[i].GetName() == name {
			return &items[i]
		}
	}

	t.Fatalf("expected rendered %s/%s", kind, name)
	return nil
}

func findContainer(t *testing.T, items []unstructured.Unstructured, kind, name, containerName string) map[string]interface{} {
	t.Helper()

	for _, item := range items {
		if item.GetKind() != kind || item.GetName() != name {
			continue
		}

		containers, found, err := unstructured.NestedSlice(item.Object, "spec", "template", "spec", "containers")
		require.NoError(t, err)
		require.True(t, found)
		for _, container := range containers {
			containerMap, ok := container.(map[string]interface{})
			require.True(t, ok)
			if containerMap["name"] == containerName {
				return containerMap
			}
		}
	}

	t.Fatalf("expected container %s in %s/%s", containerName, kind, name)
	return nil
}

func stringSlice(value interface{}) []string {
	items, ok := value.([]interface{})
	if !ok {
		if strings, ok := value.([]string); ok {
			return strings
		}
		return nil
	}

	result := make([]string, 0, len(items))
	for _, item := range items {
		if value, ok := item.(string); ok {
			result = append(result, value)
		}
	}

	return result
}

func nestedMap(t *testing.T, values map[string]interface{}, path ...string) map[string]interface{} {
	t.Helper()

	var current interface{} = values
	for _, key := range path {
		currentMap, ok := current.(map[string]interface{})
		require.True(t, ok, "expected map at %s", key)
		current = currentMap[key]
	}

	result, ok := current.(map[string]interface{})
	require.True(t, ok, "expected nested value to be map")

	return result
}

type fakePluginProvider struct {
	plugins          map[string]plugin.AcceleratorPlugin
	supportedPlugins []string
}

func newTestPluginProvider(candidateNodes ...string) fakePluginProvider {
	return newTestPluginProviderWithNodeScopeLabel(accelerator.VirtualizationNodeScopeLabel{
		Key:           plugin.NvidiaGPUVirtualizationLabelKey,
		EnabledValue:  "true",
		DisabledValue: "false",
	}, candidateNodes...)
}

func newTestPluginProviderWithNodeScopeLabel(
	label accelerator.VirtualizationNodeScopeLabel,
	candidateNodes ...string,
) fakePluginProvider {
	nvidiaPlugin := fakeAcceleratorPlugin{
		acceleratorType: string(v1.AcceleratorTypeNVIDIAGPU),
		config: &accelerator.VirtualizationConfig{
			Supported:      true,
			CandidateNodes: candidateNodes,
			NodeScopeLabel: label,
		},
	}

	return fakePluginProvider{
		plugins: map[string]plugin.AcceleratorPlugin{
			string(v1.AcceleratorTypeNVIDIAGPU): nvidiaPlugin,
		},
		supportedPlugins: []string{string(v1.AcceleratorTypeNVIDIAGPU)},
	}
}

func (f fakePluginProvider) SupportPlugins() []string {
	return f.supportedPlugins
}

func (f fakePluginProvider) GetPlugin(acceleratorType string) (plugin.AcceleratorPlugin, bool) {
	acceleratorPlugin, ok := f.plugins[acceleratorType]
	return acceleratorPlugin, ok
}

type fakeAcceleratorPlugin struct {
	acceleratorType string
	config          *accelerator.VirtualizationConfig
	err             error
}

func (p fakeAcceleratorPlugin) Handle() plugin.AcceleratorPluginHandle {
	return nil
}

func (p fakeAcceleratorPlugin) Resource() string {
	return p.acceleratorType
}

func (p fakeAcceleratorPlugin) Type() string {
	return plugin.InternalPluginType
}

func (p fakeAcceleratorPlugin) ResolveClusterVirtualizationConfig(
	context.Context,
	*v1.Cluster,
) (*accelerator.VirtualizationConfig, error) {
	return p.config, p.err
}

func newTestCluster() *v1.Cluster {
	return &v1.Cluster{
		Metadata: &v1.Metadata{Name: "cluster", Workspace: "workspace"},
		Spec: &v1.ClusterSpec{
			Type:    v1.KubernetesClusterType,
			Version: "v1.1.0",
			AcceleratorVirtualization: &v1.AcceleratorVirtualizationSpec{
				Enabled: true,
			},
		},
		Status: &v1.ClusterStatus{},
	}
}

func markHAMiOwned(cluster *v1.Cluster) {
	if cluster.Status == nil {
		cluster.Status = &v1.ClusterStatus{}
	}

	cluster.Status.ComponentStatus = map[string]*v1.ComponentStatus{
		v1.ComponentStatusAcceleratorVirtualizationKey: {
			Phase: v1.ComponentPhaseReady,
		},
	}
}

func newHAMiFakeClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, admissionregistrationv1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, rbacv1.AddToScheme(scheme))
	require.NoError(t, nodev1.AddToScheme(scheme))

	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

type hamiFakeApplyClient struct {
	client.Client
}

func (c *hamiFakeApplyClient) Patch(
	ctx context.Context,
	obj client.Object,
	patch client.Patch,
	opts ...client.PatchOption,
) error {
	if patch.Type() != types.ApplyPatchType {
		return c.Client.Patch(ctx, obj, patch, opts...)
	}

	current, ok := obj.DeepCopyObject().(client.Object)
	if !ok {
		return fmt.Errorf("deep copy of %T does not implement client.Object", obj)
	}

	err := c.Client.Get(ctx, client.ObjectKeyFromObject(obj), current)
	if apierrors.IsNotFound(err) {
		return c.Client.Create(ctx, obj)
	}
	if err != nil {
		return err
	}

	obj.SetResourceVersion(current.GetResourceVersion())

	return c.Client.Update(ctx, obj)
}

func defaultNodeScopePlan() NodeScopePlan {
	return NodeScopePlan{
		NodeScopeLabel: defaultNodeScopeLabel(),
	}
}

func nvidiaDevicePluginNodeScopePlan() NodeScopePlan {
	return NodeScopePlan{
		NodeScopeLabel: defaultNodeScopeLabel(),
		ConfigPatch: map[string]interface{}{
			"devicePlugin": map[string]interface{}{
				"enabled": true,
				"nvidiaNodeSelector": map[string]interface{}{
					"gpu":                                  nil,
					plugin.NvidiaGPUVirtualizationLabelKey: "true",
				},
			},
		},
	}
}

func newHAMiReadyDeployment(namespace string) *appsv1.Deployment {
	replicas := int32(1)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:       SchedulerName,
			Namespace:  namespace,
			Generation: 1,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
		},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 1,
			Replicas:           1,
			UpdatedReplicas:    1,
			ReadyReplicas:      1,
			AvailableReplicas:  1,
			Conditions: []appsv1.DeploymentCondition{
				{
					Type:   appsv1.DeploymentProgressing,
					Status: corev1.ConditionTrue,
				},
				{
					Type:   appsv1.DeploymentAvailable,
					Status: corev1.ConditionTrue,
				},
			},
		},
	}
}

func newHAMiReadyDaemonSet(namespace string, desired int32) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      DevicePluginDaemonSetName,
			Namespace: namespace,
		},
		Status: appsv1.DaemonSetStatus{
			DesiredNumberScheduled: desired,
			NumberReady:            desired,
			UpdatedNumberScheduled: desired,
			NumberAvailable:        desired,
			ObservedGeneration:     1,
		},
	}
}

func newHAMiDeviceConfig(namespace string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      SchedulerName + "-device",
			Namespace: namespace,
		},
	}
}

func newHAMiMonitorService(namespace string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      MonitorServiceName,
			Namespace: namespace,
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Name: "monitorport",
					Port: 31992,
				},
			},
		},
	}
}

func newHAMiTLSSecret(t *testing.T, namespace string) *corev1.Secret {
	t.Helper()

	bundle, err := generateTLSBundle(namespace, time.Now())
	require.NoError(t, err)

	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      TLSSecretName,
			Namespace: namespace,
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			corev1.TLSCertKey:       bundle.CertPEM,
			corev1.TLSPrivateKeyKey: bundle.KeyPEM,
			"ca.crt":                bundle.CAPEM,
		},
	}
}

func newHAMiWebhook(caBundle []byte) *admissionregistrationv1.MutatingWebhookConfiguration {
	return &admissionregistrationv1.MutatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name: WebhookName,
		},
		Webhooks: []admissionregistrationv1.MutatingWebhook{
			{
				Name: "hami-webhook.projecthami.io",
				ClientConfig: admissionregistrationv1.WebhookClientConfig{
					CABundle: caBundle,
				},
			},
		},
	}
}

func newHAMiNode(name string, labels map[string]string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
	}
}
