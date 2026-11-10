package plugin

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/accelerator"
)

func TestNvidiaKubernetesProfileRuntime(t *testing.T) {
	for _, tc := range []struct {
		name, nodeRuntime, configuredClass string
		driver, toolkit                    bool
		wantClass                          string
	}{
		{name: "managed containerd", nodeRuntime: "containerd://1.7.28", driver: true, toolkit: true, wantClass: "nvidia"},
		{name: "managed custom class", nodeRuntime: "containerd://1.7.28", configuredClass: "custom-nvidia", driver: true, toolkit: true, wantClass: "custom-nvidia"},
		{name: "managed CRI-O", nodeRuntime: "cri-o://1.29", driver: true, toolkit: true, wantClass: "nvidia"},
		{name: "managed Docker", nodeRuntime: "docker://26.1.4", driver: true, toolkit: true, wantClass: "nvidia"},
		{name: "driver only", nodeRuntime: "containerd://1.7.28", driver: true},
		{name: "toolkit only", nodeRuntime: "containerd://1.7.28", toolkit: true},
		{name: "unmanaged", nodeRuntime: "containerd://1.7.28"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, corev1.AddToScheme(scheme))
			scheme.AddKnownTypeWithName(schema.GroupVersionKind{Group: "nvidia.com", Version: "v1", Kind: "ClusterPolicy"}, &unstructured.Unstructured{})
			scheme.AddKnownTypeWithName(schema.GroupVersionKind{Group: "nvidia.com", Version: "v1", Kind: "ClusterPolicyList"}, &unstructured.UnstructuredList{})
			policy := &unstructured.Unstructured{Object: map[string]interface{}{
				"apiVersion": "nvidia.com/v1", "kind": "ClusterPolicy", "metadata": map[string]interface{}{"name": "custom-policy"},
				"spec": map[string]interface{}{"driver": map[string]interface{}{"enabled": tc.driver}, "toolkit": map[string]interface{}{"enabled": tc.toolkit}, "operator": map[string]interface{}{"runtimeClass": tc.configuredClass}},
			}}
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "gpu-1", Labels: map[string]string{NvidiaGPUDiscoveryLabelKey: "true"}}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{ContainerRuntimeVersion: tc.nodeRuntime}}}
			reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(policy, node).Build()
			profile, err := (&GPUAcceleratorPlugin{}).GetAcceleratorProfile(context.Background(), accelerator.AcceleratorProfileInput{})
			require.NoError(t, err)
			require.NoError(t, enrichNvidiaKubernetesProfile(context.Background(), reader, profile))
			assert.Equal(t, tc.wantClass, profile.NodeAgentRuntime.Runtime)
			assert.Equal(t, tc.wantClass, profile.MetricsExporter.Runtime.Runtime)
			if tc.driver && tc.toolkit {
				assert.Empty(t, profile.NodeAgentRuntime.InitContainers[0].Image, "Metrics binds the selected primary image")
				assert.Equal(t, profile.MetricsExporter.Image, profile.MetricsExporter.Runtime.InitContainers[0].Image)
			}
			for _, runtime := range []struct {
				selector map[string]string
				init     []corev1.Container
				volumes  []v1.ComponentVolume
			}{
				{profile.NodeAgentRuntime.NodeSelector, profile.NodeAgentRuntime.InitContainers, profile.NodeAgentRuntime.Volumes},
				{profile.MetricsExporter.Runtime.NodeSelector, profile.MetricsExporter.Runtime.InitContainers, profile.MetricsExporter.Runtime.Volumes},
			} {
				if !tc.driver || !tc.toolkit {
					assert.Empty(t, runtime.init)
					assert.NotContains(t, runtime.selector, accelerator.NvidiaGPUClientLabel)
					continue
				}
				assert.Equal(t, "true", runtime.selector[accelerator.NvidiaGPUClientLabel])
				require.Len(t, runtime.init, 1)
				assert.Equal(t, "toolkit-validation", runtime.init[0].Name)
				assert.Equal(t, []string{"until [ -f /run/nvidia/validations/toolkit-ready ]; do sleep 5; done"}, runtime.init[0].Args)
				require.NotEmpty(t, runtime.volumes)
				assert.Equal(t, v1.ComponentHostPathTypeDirectoryOrCreate, runtime.volumes[0].HostPath.Type)
				assert.Equal(t, "/run/nvidia/validations", runtime.init[0].VolumeMounts[0].MountPath)
				assert.True(t, runtime.init[0].VolumeMounts[0].ReadOnly)
			}
		})
	}
}
