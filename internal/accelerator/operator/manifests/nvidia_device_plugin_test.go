package manifests

import (
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestRenderNvidiaDevicePluginUsesMinimalTemplate(t *testing.T) {
	for _, test := range []struct {
		name, driverRoot string
	}{
		{name: "host driver"},
		{name: "GPU Operator driver", driverRoot: "/run/nvidia/driver"},
	} {
		t.Run(test.name, func(t *testing.T) {
			resources, err := RenderNvidiaDevicePlugin(NvidiaDevicePluginRenderOptions{
				Namespace: "neutree-system", ImagePrefix: "registry.example.com/neutree/", ImagePullSecret: "pull-secret",
				Version: "v2.10.0", NodeSelector: map[string]string{"neutree.ai/nvidia-vgpu-enabled": "true"}, DriverRoot: test.driverRoot,
			})
			require.NoError(t, err)

			var daemonSet appsv1.DaemonSet
			for _, object := range resources.Items {
				if object.GetKind() == "DaemonSet" {
					require.NoError(t, runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, &daemonSet))
				}
			}
			require.Equal(t, "hami-device-plugin", daemonSet.Name)
			require.Len(t, daemonSet.Spec.Template.Spec.Containers, 2)
			require.Equal(t, "registry.example.com/neutree/projecthami/hami:v2.10.0", daemonSet.Spec.Template.Spec.Containers[0].Image)
			require.Equal(t, map[string]string{"neutree.ai/nvidia-vgpu-enabled": "true"}, daemonSet.Spec.Template.Spec.NodeSelector)
			require.Equal(t, "pull-secret", daemonSet.Spec.Template.Spec.ImagePullSecrets[0].Name)

			foundDriverRoot := false
			for _, volume := range daemonSet.Spec.Template.Spec.Volumes {
				if volume.Name == "driver-root" {
					foundDriverRoot = true
					require.Equal(t, test.driverRoot, volume.HostPath.Path)
				}
			}
			require.Equal(t, test.driverRoot != "", foundDriverRoot)
		})
	}
}
