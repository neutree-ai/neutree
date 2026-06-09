package hami

import (
	"context"
	"encoding/base64"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	kubeversion "k8s.io/apimachinery/pkg/version"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
)

func TestHAMiComponentResources(t *testing.T) {
	component := NewHAMiComponent(newTestCluster(), "neutree-system", "registry.example.com/neutree/",
		"image-pull-secret", v1.KubernetesClusterConfig{}, newHAMiFakeClient(t))

	objs, err := component.GetResources()

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

func TestHAMiComponentResourcesUseHAMiEntrypoints(t *testing.T) {
	component := NewHAMiComponent(newTestCluster(), "neutree-system", "registry.example.com/neutree",
		"image-pull-secret", v1.KubernetesClusterConfig{}, newHAMiFakeClient(t))

	objs, err := component.GetResources()
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

func TestHAMiComponentRejectsMIGStrategyConfigPatch(t *testing.T) {
	cluster := newTestCluster()
	cluster.Spec.AcceleratorVirtualization.ConfigPatch = map[string]interface{}{
		"devicePlugin": map[string]interface{}{
			"migStrategy": "mixed",
		},
	}
	component := NewHAMiComponent(cluster, "neutree-system", "registry.example.com/neutree",
		"image-pull-secret", v1.KubernetesClusterConfig{}, newHAMiFakeClient(t))

	err := component.Preflight(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "MIG virtualization mode is not supported")
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

func TestHAMiComponentProtectedValuesKeepMIGStrategyDisabled(t *testing.T) {
	cluster := newTestCluster()
	cluster.Spec.AcceleratorVirtualization.ConfigPatch = map[string]interface{}{
		"devicePlugin": map[string]interface{}{
			"migStrategy": "mixed",
		},
	}
	component := NewHAMiComponent(cluster, "neutree-system", "registry.example.com/neutree",
		"image-pull-secret", v1.KubernetesClusterConfig{}, newHAMiFakeClient(t))

	values := component.buildChartValues(NodeScopePlan{})

	assert.Equal(t, "none", nestedMap(t, values, "devicePlugin")["migStrategy"])
}

func TestHAMiComponentProtectedValuesUseDefaultDeviceSplitCount(t *testing.T) {
	cluster := newTestCluster()
	cluster.Spec.AcceleratorVirtualization.ConfigPatch = map[string]interface{}{
		"devicePlugin": map[string]interface{}{
			"deviceSplitCount": 10,
		},
	}
	component := NewHAMiComponent(cluster, "neutree-system", "registry.example.com/neutree",
		"image-pull-secret", v1.KubernetesClusterConfig{}, newHAMiFakeClient(t))

	values := component.buildChartValues(NodeScopePlan{})

	assert.Equal(t, NvidiaGPUDefaultDeviceSplitCount, nestedMap(t, values, "devicePlugin")["deviceSplitCount"])
}

func TestHAMiComponentUsesGPUTopologyAwareSchedulerPolicy(t *testing.T) {
	component := NewHAMiComponent(newTestCluster(), "neutree-system", "registry.example.com/neutree",
		"image-pull-secret", v1.KubernetesClusterConfig{}, newHAMiFakeClient(t))

	values := component.buildChartValues(NodeScopePlan{
		ConfigPatch: map[string]interface{}{
			"scheduler": map[string]interface{}{
				"defaultSchedulerPolicy": map[string]interface{}{
					"gpuSchedulerPolicy": plugin.NvidiaGPUTopologyAwarePolicy,
				},
			},
		},
	})

	defaultSchedulerPolicy := nestedMap(t, values, "scheduler", "defaultSchedulerPolicy")
	assert.Equal(t, plugin.NvidiaGPUTopologyAwarePolicy, defaultSchedulerPolicy["gpuSchedulerPolicy"])
}

func TestHAMiComponentStatusReadyWhenDaemonSetAndNodeScopeAreReady(t *testing.T) {
	tlsSecret := newHAMiTLSSecret(t, "neutree-system")
	fakeClient := newHAMiFakeClient(t,
		newHAMiReadyDeployment("neutree-system"),
		newHAMiReadyDaemonSet("neutree-system", 2),
		newHAMiMonitorService("neutree-system"),
		tlsSecret,
		newHAMiWebhook(string(tlsSecret.Data["ca.crt"])),
		newHAMiNode("gpu-1", map[string]string{
			NvidiaVGPUEnabledLabelKey: "true",
			"nvidia.com/gpu.present":  "true",
		}),
		newHAMiNode("gpu-2", map[string]string{
			NvidiaVGPUEnabledLabelKey: "true",
			"nvidia.com/gpu.present":  "true",
		}),
	)
	component := NewHAMiComponent(newTestCluster(), "neutree-system", "registry.example.com/neutree/",
		"image-pull-secret", v1.KubernetesClusterConfig{}, fakeClient, newTestPluginProvider("gpu-1", "gpu-2"))

	status, err := component.CheckResourcesStatus(context.Background())

	require.NoError(t, err)
	assert.True(t, status.Ready)
	assert.Equal(t, 2, status.ReadyNodes)
	assert.Equal(t, 2, status.DesiredNodes)
}

func TestHAMiComponentReconcileWritesNotReadyStatusWhenDaemonSetMissing(t *testing.T) {
	cluster := newTestCluster()
	cluster.Status = &v1.ClusterStatus{
		ComponentStatus: map[string]*v1.ComponentStatus{
			legacyComponentStatusHAMiKey: {
				Phase: v1.ComponentPhaseReady,
			},
		},
	}
	tlsSecret := newHAMiTLSSecret(t, "neutree-system")
	component := NewHAMiComponent(cluster, "neutree-system", "registry.example.com/neutree/",
		"image-pull-secret", v1.KubernetesClusterConfig{}, newHAMiFakeClient(t,
			newHAMiReadyDeployment("neutree-system"),
			tlsSecret,
			newHAMiWebhook(string(tlsSecret.Data["ca.crt"])),
			newHAMiNode("gpu-1", map[string]string{
				NvidiaVGPUEnabledLabelKey: "true",
				"nvidia.com/gpu.present":  "true",
			}),
		), newTestPluginProvider("gpu-1"))

	err := component.UpdateStatus(context.Background())

	require.NoError(t, err)
	require.NotNil(t, cluster.Status.ComponentStatus[v1.ComponentStatusAcceleratorVirtualizationKey])
	assert.Equal(t, v1.ComponentPhaseNotReady, cluster.Status.ComponentStatus[v1.ComponentStatusAcceleratorVirtualizationKey].Phase)
	assert.Equal(t, "DaemonSetNotReady", cluster.Status.ComponentStatus[v1.ComponentStatusAcceleratorVirtualizationKey].Reason)
	assert.Nil(t, cluster.Status.ComponentStatus[legacyComponentStatusHAMiKey])
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
		config: &plugin.VirtualizationConfig{
			Supported:      true,
			CandidateNodes: []string{"plugin-candidate"},
			NodeScopeLabel: plugin.VirtualizationNodeScopeLabel{
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

func TestHAMiMigrationDeletesManagedWorkloadsWithChangedSelector(t *testing.T) {
	existingDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      SchedulerName,
			Namespace: "neutree-system",
			Labels: map[string]string{
				ManagedComponentLabelKey: ManagedComponentLabelValue,
			},
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "hami-scheduler"},
			},
		},
	}
	existingDaemonSet := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      DevicePluginDaemonSetName,
			Namespace: "neutree-system",
			Labels: map[string]string{
				ManagedComponentLabelKey: ManagedComponentLabelValue,
			},
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "hami-device-plugin"},
			},
		},
	}
	fakeClient := newHAMiFakeClient(t, existingDeployment, existingDaemonSet)
	component := NewHAMiComponent(newTestCluster(), "neutree-system", "registry.example.com/neutree/",
		"image-pull-secret", v1.KubernetesClusterConfig{}, fakeClient)
	objs, err := component.renderResources(NodeScopePlan{})
	require.NoError(t, err)

	err = component.replaceWorkloadsWithImmutableSelectorChanges(context.Background(), objs)

	require.NoError(t, err)
	err = fakeClient.Get(context.Background(), client.ObjectKey{Name: SchedulerName, Namespace: "neutree-system"},
		&appsv1.Deployment{})
	assert.True(t, apierrors.IsNotFound(err))
	err = fakeClient.Get(context.Background(), client.ObjectKey{Name: DevicePluginDaemonSetName, Namespace: "neutree-system"},
		&appsv1.DaemonSet{})
	assert.True(t, apierrors.IsNotFound(err))
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
	assert.Contains(t, err.Error(), "DRA is not supported")
}

func TestHAMiPreflightRejectsUnmanagedWebhook(t *testing.T) {
	component := NewHAMiComponent(newTestCluster(), "neutree-system", "registry.example.com/neutree/",
		"image-pull-secret", v1.KubernetesClusterConfig{}, newHAMiFakeClient(t, newHAMiWebhook("")))

	err := component.Preflight(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmanaged HAMi webhook")
}

func TestHAMiPreflightRejectsUnmanagedDaemonSet(t *testing.T) {
	component := NewHAMiComponent(newTestCluster(), "neutree-system", "registry.example.com/neutree/",
		"image-pull-secret", v1.KubernetesClusterConfig{}, newHAMiFakeClient(t, &appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      DevicePluginDaemonSetName,
				Namespace: "neutree-system",
			},
		}))

	err := component.Preflight(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmanaged HAMi resource DaemonSet/hami-device-plugin")
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

func TestHAMiPatchWebhookCABundleWritesBase64CA(t *testing.T) {
	tlsSecret := newHAMiTLSSecret(t, "neutree-system")
	fakeClient := newHAMiFakeClient(t, tlsSecret, newHAMiWebhook(""))
	component := NewHAMiComponent(newTestCluster(), "neutree-system", "registry.example.com/neutree/",
		"image-pull-secret", v1.KubernetesClusterConfig{}, fakeClient)

	err := component.PatchWebhookCABundle(context.Background())

	require.NoError(t, err)
	webhook := newHAMiWebhook("")
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKey{Name: WebhookName}, webhook))
	webhooks, found, err := unstructured.NestedSlice(webhook.Object, "webhooks")
	require.NoError(t, err)
	require.True(t, found)
	actual, found, err := unstructured.NestedString(webhooks[0].(map[string]interface{}), "clientConfig", "caBundle")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, base64.StdEncoding.EncodeToString(tlsSecret.Data["ca.crt"]), actual)
}

func TestHAMiDeleteRemovesTLSSecret(t *testing.T) {
	tlsSecret := newHAMiTLSSecret(t, "neutree-system")
	fakeClient := newHAMiFakeClient(t, tlsSecret)
	component := NewHAMiComponent(newTestCluster(), "neutree-system", "registry.example.com/neutree/",
		"image-pull-secret", v1.KubernetesClusterConfig{}, fakeClient)

	err := component.Delete()

	require.NoError(t, err)
	got := &corev1.Secret{}
	err = fakeClient.Get(context.Background(), client.ObjectKey{Name: TLSSecretName, Namespace: "neutree-system"}, got)
	assert.True(t, apierrors.IsNotFound(err))
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
	nvidiaPlugin := fakeAcceleratorPlugin{
		acceleratorType: string(v1.AcceleratorTypeNVIDIAGPU),
		config: &plugin.VirtualizationConfig{
			Supported:      true,
			CandidateNodes: candidateNodes,
			NodeScopeLabel: plugin.VirtualizationNodeScopeLabel{
				Key:           plugin.NvidiaGPUVirtualizationLabelKey,
				EnabledValue:  "true",
				DisabledValue: "false",
			},
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
	config          *plugin.VirtualizationConfig
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
) (*plugin.VirtualizationConfig, error) {
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

func newHAMiFakeClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, rbacv1.AddToScheme(scheme))

	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
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

func newHAMiWebhook(caBundle string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "admissionregistration.k8s.io/v1",
			"kind":       "MutatingWebhookConfiguration",
			"metadata": map[string]interface{}{
				"name": WebhookName,
			},
			"webhooks": []interface{}{
				map[string]interface{}{
					"name": "hami-webhook.projecthami.io",
					"clientConfig": map[string]interface{}{
						"caBundle": caBundle,
					},
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
