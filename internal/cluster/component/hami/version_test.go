package hami

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	kubeversion "k8s.io/apimachinery/pkg/version"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestVersionForCluster(test *testing.T) {
	cases := []struct {
		clusterVersion string
		hamiVersion    string
	}{
		{clusterVersion: "v1.1.0-rc.1", hamiVersion: "v2.9.0"},
		{clusterVersion: "v1.1.1", hamiVersion: "v2.9.0"},
		{clusterVersion: "1.2.0", hamiVersion: "v2.9.0"},
		{clusterVersion: " v1.2.99-rc.1+build.1 ", hamiVersion: "v2.9.0"},
		{clusterVersion: "v1.3.0-rc.1", hamiVersion: "v2.10.0"},
		{clusterVersion: "1.3.0+build.1", hamiVersion: "v2.10.0"},
		{clusterVersion: "v1.3.1", hamiVersion: "v2.10.0"},
		{clusterVersion: "v1.4.0", hamiVersion: "v2.10.0"},
		{clusterVersion: ""},
		{clusterVersion: " "},
		{clusterVersion: "invalid"},
		{clusterVersion: "v1.0.9"},
	}
	for _, testCase := range cases {
		test.Run(testCase.clusterVersion, func(test *testing.T) {
			hamiVersion, err := VersionForCluster(testCase.clusterVersion)
			if testCase.hamiVersion == "" {
				require.Error(test, err)
				assert.Empty(test, hamiVersion)
				return
			}
			require.NoError(test, err)
			assert.Equal(test, testCase.hamiVersion, hamiVersion)
		})
	}
}

func TestHAMiClusterVersionCompatibility(test *testing.T) {
	originalGetKubernetesServerVersion := getKubernetesServerVersion
	getKubernetesServerVersion = func(*v1.Cluster) (*kubeversion.Info, error) {
		return &kubeversion.Info{GitVersion: "v1.35.2+k3s1"}, nil
	}
	test.Cleanup(func() {
		getKubernetesServerVersion = originalGetKubernetesServerVersion
	})

	cases := []struct {
		clusterVersion   string
		hamiVersion      string
		schedulerVersion string
	}{
		{clusterVersion: "v1.1.0", hamiVersion: "v2.9.0", schedulerVersion: "v1.35.2"},
		{clusterVersion: "v1.2.0", hamiVersion: "v2.9.0", schedulerVersion: "v1.35.2"},
		{clusterVersion: "v1.2.1", hamiVersion: "v2.9.0", schedulerVersion: "v1.35.2"},
		{clusterVersion: "v1.3.0-rc.1+build", hamiVersion: "v2.10.0", schedulerVersion: "v1.35.9"},
		{clusterVersion: "v1.3.0", hamiVersion: "v2.10.0", schedulerVersion: "v1.35.9"},
		{clusterVersion: "1.4.0", hamiVersion: "v2.10.0", schedulerVersion: "v1.35.9"},
	}
	for _, testCase := range cases {
		test.Run(testCase.clusterVersion, func(test *testing.T) {
			cluster := newTestCluster()
			cluster.Spec.Version = testCase.clusterVersion
			component := NewHAMiComponent(cluster, "neutree-system", "registry.example.com/neutree",
				"image-pull-secret", v1.KubernetesClusterConfig{}, newHAMiFakeClient(test), newTestPluginProvider("gpu-1"))
			resources, err := component.renderResources(nvidiaDevicePluginNodeScopePlan())
			require.NoError(test, err)

			deployment := findObject(test, resources.Items, "Deployment", SchedulerName)
			assert.Equal(test, "hami-"+strings.TrimPrefix(testCase.hamiVersion, "v"), deployment.GetLabels()["helm.sh/chart"])
			extender := findContainer(test, resources.Items, "Deployment", SchedulerName, "vgpu-scheduler-extender")
			assert.Equal(test, "registry.example.com/neutree/projecthami/hami:"+testCase.hamiVersion, extender["image"])
			devicePlugin := findContainer(test, resources.Items, "DaemonSet", DevicePluginDaemonSetName, "device-plugin")
			assert.Equal(test, "registry.example.com/neutree/projecthami/hami:"+testCase.hamiVersion, devicePlugin["image"])
			monitor := findContainer(test, resources.Items, "DaemonSet", DevicePluginDaemonSetName, "vgpu-monitor")
			assert.Equal(test, "registry.example.com/neutree/projecthami/hami:"+testCase.hamiVersion, monitor["image"])
			scheduler := findContainer(test, resources.Items, "Deployment", SchedulerName, "kube-scheduler")
			assert.Equal(test, "registry.example.com/neutree/kube-scheduler:"+testCase.schedulerVersion, scheduler["image"])
			for _, resource := range resources.Items {
				assert.NotContains(test, strings.ToLower(resource.GetName()), "dra")
				assert.NotEqual(test, "ResourceClaim", resource.GetKind())
				assert.NotEqual(test, "ResourceSlice", resource.GetKind())
			}

			require.NoError(test, component.UpdateStatus(context.Background()))
			status := cluster.Status.ComponentStatus[v1.ComponentStatusAcceleratorVirtualizationKey]
			assert.Equal(test, testCase.hamiVersion, status.Version)
			assert.Equal(test, v1.ComponentPhaseNotReady, status.Phase)
			component.setNotReadyStatus("ApplyFailed", "test failure")
			assert.Equal(test, testCase.hamiVersion,
				cluster.Status.ComponentStatus[v1.ComponentStatusAcceleratorVirtualizationKey].Version)
		})
	}
}

func TestHAMiLegacyChartProtectedDRA(test *testing.T) {
	cluster := newTestCluster()
	cluster.Spec.Version = "v1.2.0"
	component := NewHAMiComponent(cluster, "neutree-system", "docker.io", "",
		v1.KubernetesClusterConfig{}, newHAMiFakeClient(test))
	scopePlan := NodeScopePlan{ConfigPatch: map[string]interface{}{
		"dra": map[string]interface{}{"enabled": true},
	}}
	values := component.buildChartValues(scopePlan, LegacyVersion)
	assert.Equal(test, false, nestedMap(test, values, "dra")["enabled"])
	resources, err := component.renderResources(scopePlan)
	require.NoError(test, err)
	for _, resource := range resources.Items {
		assert.NotContains(test, strings.ToLower(resource.GetName()), "dra")
	}
}

func TestHAMiRejectsUnsupportedClusterVersions(test *testing.T) {
	for _, clusterVersion := range []string{"", " ", "invalid", "v1.0.9"} {
		test.Run(clusterVersion, func(test *testing.T) {
			cluster := newTestCluster()
			cluster.Spec.Version = clusterVersion
			component := NewHAMiComponent(cluster, "neutree-system", "docker.io", "",
				v1.KubernetesClusterConfig{}, newHAMiFakeClient(test), newTestPluginProvider("gpu-1"))
			_, err := component.renderResources(defaultNodeScopePlan())
			require.Error(test, err)
			_, err = component.CheckResourcesStatus(context.Background())
			require.Error(test, err)
			require.NoError(test, component.UpdateStatus(context.Background()))
			status := cluster.Status.ComponentStatus[v1.ComponentStatusAcceleratorVirtualizationKey]
			assert.Empty(test, status.Version)
			assert.Equal(test, v1.ComponentPhaseNotReady, status.Phase)
			require.Error(test, component.Reconcile())
			assert.Empty(test, cluster.Status.ComponentStatus[v1.ComponentStatusAcceleratorVirtualizationKey].Version)
		})
	}
}

func TestHAMiReconcileClusterVersionUpgrade(test *testing.T) {
	originalGetKubernetesServerVersion := getKubernetesServerVersion
	getKubernetesServerVersion = func(*v1.Cluster) (*kubeversion.Info, error) {
		return &kubeversion.Info{GitVersion: "v1.35.2"}, nil
	}
	test.Cleanup(func() {
		getKubernetesServerVersion = originalGetKubernetesServerVersion
	})

	ctx := context.Background()
	cluster := newTestCluster()
	cluster.Spec.Version = "v1.2.0"
	fakeClient := &hamiFakeApplyClient{Client: newHAMiFakeClient(test, newHAMiNode("gpu-1", nil))}
	component := NewHAMiComponent(cluster, "neutree-system", "docker.io", "",
		v1.KubernetesClusterConfig{}, fakeClient, newTestPluginProvider("gpu-1"))
	require.Error(test, component.Reconcile())
	assert.Equal(test, "v2.9.0", cluster.Status.ComponentStatus[v1.ComponentStatusAcceleratorVirtualizationKey].Version)

	deployment := &appsv1.Deployment{}
	deploymentKey := client.ObjectKey{Namespace: "neutree-system", Name: SchedulerName}
	require.NoError(test, fakeClient.Get(ctx, deploymentKey, deployment))
	deployment.Status = newHAMiReadyDeployment("neutree-system").Status
	require.NoError(test, fakeClient.Status().Update(ctx, deployment))
	secret := &corev1.Secret{}
	secretKey := client.ObjectKey{Namespace: "neutree-system", Name: TLSSecretName}
	require.NoError(test, fakeClient.Get(ctx, secretKey, secret))
	originalCA := append([]byte(nil), secret.Data["ca.crt"]...)

	steps := []struct {
		name             string
		clusterVersion   string
		hamiVersion      string
		schedulerVersion string
	}{
		{name: "control plane reconcile preserves legacy", clusterVersion: "v1.2.0", hamiVersion: "v2.9.0", schedulerVersion: "v1.35.2"},
		{name: "legacy patch upgrade preserves legacy", clusterVersion: "v1.2.1", hamiVersion: "v2.9.0", schedulerVersion: "v1.35.2"},
		{name: "explicit cluster upgrade selects new HAMi", clusterVersion: "v1.3.0-rc.1", hamiVersion: "v2.10.0", schedulerVersion: "v1.35.9"},
		{name: "repeated reconcile preserves new HAMi", clusterVersion: "v1.3.0", hamiVersion: "v2.10.0", schedulerVersion: "v1.35.9"},
	}
	for _, step := range steps {
		test.Run(step.name, func(test *testing.T) {
			cluster.Spec.Version = step.clusterVersion
			require.NoError(test, component.Reconcile())
			require.NoError(test, component.UpdateStatus(ctx))
			status := cluster.Status.ComponentStatus[v1.ComponentStatusAcceleratorVirtualizationKey]
			assert.Equal(test, v1.ComponentPhaseReady, status.Phase)
			assert.Equal(test, step.hamiVersion, status.Version)
			require.NoError(test, fakeClient.Get(ctx, deploymentKey, deployment))
			assert.Equal(test, "hami-"+strings.TrimPrefix(step.hamiVersion, "v"), deployment.Labels["helm.sh/chart"])
			containerImages := make(map[string]string, len(deployment.Spec.Template.Spec.Containers))
			for _, container := range deployment.Spec.Template.Spec.Containers {
				containerImages[container.Name] = container.Image
			}
			assert.Equal(test, "docker.io/projecthami/hami:"+step.hamiVersion, containerImages["vgpu-scheduler-extender"])
			assert.Equal(test, "registry.k8s.io/kube-scheduler:"+step.schedulerVersion, containerImages["kube-scheduler"])
			require.NoError(test, fakeClient.Get(ctx, secretKey, secret))
			assert.Equal(test, originalCA, secret.Data["ca.crt"])
			webhook := &admissionregistrationv1.MutatingWebhookConfiguration{}
			require.NoError(test, fakeClient.Get(ctx, client.ObjectKey{Name: WebhookName}, webhook))
			require.NotEmpty(test, webhook.Webhooks)
			assert.Equal(test, originalCA, webhook.Webhooks[0].ClientConfig.CABundle)
		})
	}
}
