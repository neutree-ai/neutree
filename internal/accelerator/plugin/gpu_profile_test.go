package plugin

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/accelerator"
)

func TestNvidiaKubernetesProfileRuntime(t *testing.T) {
	for _, tc := range []struct {
		name, configuredClass, existingClass       string
		driver, toolkit, noGPU, noPolicy, failRead bool
		wantClass, wantError                       string
	}{
		{name: "managed custom class", configuredClass: "custom-nvidia", existingClass: "custom-nvidia", driver: true, toolkit: true, wantClass: "custom-nvidia"},
		{name: "managed default class", existingClass: "nvidia", driver: true, toolkit: true, wantClass: "nvidia"},
		{name: "missing operator class uses default", configuredClass: "pending-nvidia", existingClass: "nvidia", driver: true, toolkit: true, wantClass: "nvidia"},
		{name: "driver only still uses class", existingClass: "nvidia", driver: true, wantClass: "nvidia"},
		{name: "toolkit only still uses class", existingClass: "nvidia", toolkit: true, wantClass: "nvidia"},
		{name: "unmanaged still uses class", existingClass: "nvidia", wantClass: "nvidia"},
		{name: "no operator uses default", noPolicy: true, existingClass: "nvidia", wantClass: "nvidia"},
		{name: "no operator uses owned fallback", noPolicy: true, existingClass: accelerator.NeutreeNvidiaRuntimeClass, wantClass: accelerator.NeutreeNvidiaRuntimeClass},
		{name: "GPU profile requires class", wantError: "NVIDIA RuntimeClass is unavailable"},
		{name: "other hardware does not require NVIDIA class", noGPU: true},
		{name: "other hardware does not query NVIDIA classes", noGPU: true, failRead: true},
		{name: "class query failure", existingClass: "nvidia", failRead: true, wantError: "runtimeclasses"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, corev1.AddToScheme(scheme))
			require.NoError(t, nodev1.AddToScheme(scheme))
			scheme.AddKnownTypeWithName(schema.GroupVersionKind{Group: "nvidia.com", Version: "v1", Kind: "ClusterPolicy"}, &unstructured.Unstructured{})
			scheme.AddKnownTypeWithName(schema.GroupVersionKind{Group: "nvidia.com", Version: "v1", Kind: "ClusterPolicyList"}, &unstructured.UnstructuredList{})
			policy := &unstructured.Unstructured{Object: map[string]interface{}{
				"apiVersion": "nvidia.com/v1", "kind": "ClusterPolicy", "metadata": map[string]interface{}{"name": "custom-policy"},
				"spec": map[string]interface{}{
					"driver": map[string]interface{}{"enabled": tc.driver}, "toolkit": map[string]interface{}{"enabled": tc.toolkit},
					"operator": map[string]interface{}{"runtimeClass": tc.configuredClass},
				},
			}}
			objects := []client.Object{}
			if !tc.noPolicy {
				objects = append(objects, policy)
			}
			if !tc.noGPU {
				objects = append(objects, &corev1.Node{ObjectMeta: metav1.ObjectMeta{
					Name: "gpu-1", Labels: map[string]string{NvidiaGPUDiscoveryLabelKey: "true"},
				}})
			}
			if tc.existingClass != "" {
				class := &nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: tc.existingClass}, Handler: "nvidia"}
				if tc.existingClass == accelerator.NeutreeNvidiaRuntimeClass {
					class.Labels = map[string]string{
						v1.LabelManagedBy:         v1.LabelManagedByValue,
						v1.NeutreeClusterLabelKey: "cluster-a", v1.NeutreeClusterWorkspaceLabelKey: "workspace-a",
					}
				}
				objects = append(objects, class)
			}
			reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithInterceptorFuncs(interceptor.Funcs{
				Get: func(ctx context.Context, reader client.WithWatch, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
					if _, runtimeClass := object.(*nodev1.RuntimeClass); tc.failRead && runtimeClass {
						return apierrors.NewForbidden(schema.GroupResource{Group: "node.k8s.io", Resource: "runtimeclasses"}, key.Name, errors.New("denied"))
					}
					return reader.Get(ctx, key, object, opts...)
				},
			}).Build()
			profile, err := (&GPUAcceleratorPlugin{}).GetAcceleratorProfile(context.Background(), accelerator.AcceleratorProfileInput{})
			require.NoError(t, err)
			err = enrichNvidiaKubernetesProfile(context.Background(), reader, &v1.Cluster{Metadata: &v1.Metadata{Name: "cluster-a", Workspace: "workspace-a"}}, profile)
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
				return
			}
			require.NoError(t, err)
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
				assert.Equal(t, accelerator.NvidiaGPUDiscoveryLabelValue, runtime.selector[accelerator.NvidiaGPUDiscoveryLabelKey])
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
