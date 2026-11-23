package hami

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
	"github.com/neutree-ai/neutree/pkg/accelerator"
)

func TestHAMiGPUOperatorChartValues(t *testing.T) {
	for _, tc := range []struct {
		name, clusterVersion string
		managed              bool
		wantClass            string
	}{
		{name: "2.9 managed", clusterVersion: "v1.2.0", managed: true, wantClass: "custom-nvidia"},
		{name: "2.10 managed", clusterVersion: "v1.3.0", managed: true, wantClass: "custom-nvidia"},
		{name: "2.9 OS driver fallback", clusterVersion: "v1.2.0", wantClass: "neutree-nvidia"},
		{name: "2.10 OS driver fallback", clusterVersion: "v1.3.0", wantClass: "neutree-nvidia"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := plugin.VirtualizationConfigInput{RuntimeClassName: tc.wantClass, Nodes: []corev1.Node{{ObjectMeta: metav1.ObjectMeta{
				Name: "gpu", Labels: map[string]string{accelerator.NvidiaGPUDiscoveryLabelKey: "true"},
			}}}}
			if tc.managed {
				input.GPUOperatorClusterPolicies = []plugin.GPUOperatorClusterPolicy{{Name: "policy", Spec: map[string]interface{}{
					"driver": map[string]interface{}{"enabled": true}, "toolkit": map[string]interface{}{"enabled": true},
					"devicePlugin": map[string]interface{}{"enabled": false}, "operator": map[string]interface{}{"runtimeClass": "custom-nvidia"},
				}}}
			}
			config, err := (&plugin.GPUAcceleratorPlugin{}).ResolveVirtualizationConfig(context.Background(), input)
			require.NoError(t, err)
			cluster := newTestCluster()
			cluster.Spec.Version = tc.clusterVersion
			component := NewHAMiComponent(cluster, "neutree-system", "docker.io", "", v1.KubernetesClusterConfig{}, newHAMiFakeClient(t))
			resources, err := component.renderResources(NodeScopePlan{ConfigPatch: config.ConfigPatch})
			require.NoError(t, err)
			ds := &appsv1.DaemonSet{}
			require.NoError(t, runtime.DefaultUnstructuredConverter.FromUnstructured(findObject(t, resources.Items, "DaemonSet", DevicePluginDaemonSetName).Object, ds))
			pod := ds.Spec.Template.Spec
			require.NotNil(t, pod.RuntimeClassName)
			require.Equal(t, tc.wantClass, *pod.RuntimeClassName)
			require.Equal(t, "true", pod.NodeSelector[plugin.NvidiaGPUVirtualizationLabelKey])
			if tc.managed {
				require.Equal(t, "true", pod.NodeSelector[accelerator.NvidiaGPUClientLabel])
				require.Len(t, pod.InitContainers, 1)
				require.Contains(t, pod.InitContainers[0].Args[0], accelerator.NvidiaValidationPath+"/toolkit-ready")
				require.NotEmpty(t, pod.InitContainers[0].VolumeMounts)
			} else {
				require.NotContains(t, pod.NodeSelector, accelerator.NvidiaGPUClientLabel)
				require.Empty(t, pod.InitContainers)
			}
		})
	}
}
