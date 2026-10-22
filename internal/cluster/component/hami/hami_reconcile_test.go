package hami

import (
	"context"
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

func TestHAMiReconcileInstallsAndUpgradesEmbeddedChart(test *testing.T) {
	originalGetKubernetesServerVersion := getKubernetesServerVersion
	getKubernetesServerVersion = func(*v1.Cluster) (*kubeversion.Info, error) {
		return &kubeversion.Info{GitVersion: "v1.33.13"}, nil
	}
	test.Cleanup(func() {
		getKubernetesServerVersion = originalGetKubernetesServerVersion
	})

	cases := []struct {
		name    string
		upgrade bool
	}{
		{name: "fresh installation"},
		{name: "upgrade from HAMi 2.9.0", upgrade: true},
	}
	for _, testCase := range cases {
		test.Run(testCase.name, func(test *testing.T) {
			ctx := context.Background()
			cluster := newTestCluster()
			objects := []client.Object{newHAMiNode("gpu-1", nil)}
			if testCase.upgrade {
				cluster.Status.ComponentStatus = map[string]*v1.ComponentStatus{
					v1.ComponentStatusAcceleratorVirtualizationKey: {
						Phase: v1.ComponentPhaseReady, Managed: true, Version: "v2.9.0",
					},
				}
				deployment := newHAMiReadyDeployment("neutree-system")
				deployment.Spec.Template.Spec.Containers = []corev1.Container{
					{Name: "vgpu-scheduler-extender", Image: "docker.io/projecthami/hami:v2.9.0"},
				}
				objects = append(objects, deployment, newHAMiTLSSecret(test, "neutree-system"))
			}

			fakeClient := &hamiFakeApplyClient{Client: newHAMiFakeClient(test, objects...)}
			component := NewHAMiComponent(cluster, "neutree-system", "docker.io", "",
				v1.KubernetesClusterConfig{}, fakeClient, newTestPluginProvider("gpu-1"))
			err := component.Reconcile()
			if testCase.upgrade {
				require.NoError(test, err)
			} else {
				require.Error(test, err)
				assert.Equal(test, v1.ComponentPhaseNotReady,
					cluster.Status.ComponentStatus[v1.ComponentStatusAcceleratorVirtualizationKey].Phase)
			}

			deployment := &appsv1.Deployment{}
			key := client.ObjectKey{Namespace: "neutree-system", Name: SchedulerName}
			require.NoError(test, fakeClient.Get(ctx, key, deployment))
			require.NotNil(test, deployment.Spec.Strategy.RollingUpdate)
			assert.Equal(test, int32(0), deployment.Spec.Strategy.RollingUpdate.MaxUnavailable.IntVal)
			assert.Equal(test, int32(1), deployment.Spec.Strategy.RollingUpdate.MaxSurge.IntVal)
			containerImages := make(map[string]string, len(deployment.Spec.Template.Spec.Containers))
			for _, container := range deployment.Spec.Template.Spec.Containers {
				containerImages[container.Name] = container.Image
			}
			assert.Equal(test, "docker.io/projecthami/hami:v2.10.0", containerImages["vgpu-scheduler-extender"])
			assert.Equal(test, "registry.k8s.io/kube-scheduler:v1.33.13", containerImages["kube-scheduler"])

			secret := &corev1.Secret{}
			require.NoError(test, fakeClient.Get(ctx, client.ObjectKey{Namespace: "neutree-system", Name: TLSSecretName}, secret))
			originalCA := append([]byte(nil), secret.Data["ca.crt"]...)
			deployment.Status = newHAMiReadyDeployment("neutree-system").Status
			require.NoError(test, fakeClient.Status().Update(ctx, deployment))
			require.NoError(test, component.Reconcile())
			require.NoError(test, component.Reconcile())

			status := cluster.Status.ComponentStatus[v1.ComponentStatusAcceleratorVirtualizationKey]
			assert.Equal(test, v1.ComponentPhaseReady, status.Phase)
			assert.Equal(test, "v2.10.0", status.Version)
			require.NoError(test, fakeClient.Get(ctx, client.ObjectKeyFromObject(secret), secret))
			assert.Equal(test, originalCA, secret.Data["ca.crt"])
			webhook := &admissionregistrationv1.MutatingWebhookConfiguration{}
			require.NoError(test, fakeClient.Get(ctx, client.ObjectKey{Name: WebhookName}, webhook))
			require.NotEmpty(test, webhook.Webhooks)
			assert.Equal(test, originalCA, webhook.Webhooks[0].ClientConfig.CABundle)
		})
	}
}
