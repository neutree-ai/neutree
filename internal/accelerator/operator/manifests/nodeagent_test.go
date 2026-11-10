package manifests_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator/operator"
	"github.com/neutree-ai/neutree/internal/accelerator/operator/manifests"
	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
	publicaccelerator "github.com/neutree-ai/neutree/pkg/accelerator"
)

func TestRenderNvidiaNodeAgentContracts(t *testing.T) {
	for _, test := range []struct {
		name, version string
		mode          v1.ClusterAcceleratorExporterMode
		legacy        bool
	}{
		{name: "managed current contract", version: "v1.2.0", mode: v1.ClusterAcceleratorExporterModeManaged},
		{name: "legacy managed contract", version: "v1.1.1", mode: v1.ClusterAcceleratorExporterModeManaged, legacy: true},
		{name: "legacy external contract", version: "v1.1.1", mode: v1.ClusterAcceleratorExporterModeExternal, legacy: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			profile, err := (&plugin.GPUAcceleratorPlugin{}).GetAcceleratorProfile(context.Background())
			require.NoError(t, err)
			cluster := &v1.Cluster{Metadata: &v1.Metadata{Name: "cluster-a", Workspace: "workspace-a"}, Spec: &v1.ClusterSpec{
				Version: test.version, Config: &v1.ClusterConfig{Metrics: &v1.ClusterMetricsConfig{
					AcceleratorExporter: &v1.ClusterAcceleratorExporterConfig{Mode: test.mode},
				}},
			}}
			resources, err := manifests.RenderNvidia(context.Background(), manifests.NvidiaRenderOptions{Cluster: cluster, Namespace: "neutree-system", Profile: profile})
			require.NoError(t, err)
			container := renderedNodeAgent(t, resources).Spec.Template.Spec.Containers[0]
			env := literalEnv(container.Env)
			assert.Equal(t, "all", env["NVIDIA_VISIBLE_DEVICES"])

			if test.legacy {
				assert.Equal(t, "neutree/neutree-node-agent:v1.1.0-rc.1", container.Image)
				assert.Equal(t, []string{"--listen-address=:19101", "--cluster-type=kubernetes", "--metrics-mode=" + string(test.mode), "--node=$(NODE_NAME)", "--node-ip=$(NODE_IP)"}, container.Args)
				assert.NotContains(t, env, v1.VirtualizationMetricsTargetEnvKey)
				assert.NotContains(t, env, v1.AcceleratorExporterNamespaceEnvKey)
				assert.NotContains(t, env, v1.AcceleratorExporterPodSelectorEnvKey)
				return
			}

			require.Len(t, container.VolumeMounts, 1)
			assert.Contains(t, container.Args, "--accelerator-exporter-port=19400")
			assert.Contains(t, container.Args, "--accelerator-exporter-metrics-path=/metrics")
			assert.Equal(t, "neutree-system", env[v1.AcceleratorExporterNamespaceEnvKey])
			var selector map[string]string
			require.NoError(t, json.Unmarshal([]byte(env[v1.AcceleratorExporterPodSelectorEnvKey]), &selector))
			assert.Equal(t, manifests.ExporterName, selector["app"])
		})
	}
}

func TestNvidiaRendererNodeAgentKeepsSharedScope(t *testing.T) {
	profile, err := (&plugin.GPUAcceleratorPlugin{}).GetAcceleratorProfile(context.Background())
	require.NoError(t, err)
	renderer := operator.NewNvidiaResourceRenderer(nodeAgentProfiles{profile: profile})
	resources, err := renderer.Render(context.Background(), &operator.OperatorContext{
		Cluster: &v1.Cluster{Metadata: &v1.Metadata{Name: "cluster-a", Workspace: "workspace-a"}, Spec: &v1.ClusterSpec{Version: "v1.2.0"}},
		Client:  fake.NewClientBuilder().Build(), Namespace: "neutree-system",
	}, operator.ProviderState{}, operator.ComponentPlan{})
	require.NoError(t, err)
	pod := renderedNodeAgent(t, resources).Spec.Template.Spec
	assert.Empty(t, pod.NodeSelector)
	assert.Empty(t, pod.InitContainers)
	assert.Nil(t, pod.RuntimeClassName)
	assert.Nil(t, pod.Affinity)

	for _, managed := range []bool{false, true} {
		t.Run(map[bool]string{false: "OS driver", true: "GPU Operator driver"}[managed], func(t *testing.T) {
			resources, err := renderer.Render(context.Background(), &operator.OperatorContext{
				Cluster: &v1.Cluster{Metadata: &v1.Metadata{Name: "cluster-a", Workspace: "workspace-a"}, Spec: &v1.ClusterSpec{Version: "v1.2.0"}},
				Client:  fake.NewClientBuilder().Build(), Namespace: "neutree-system",
			}, operator.ProviderState{Managed: managed}, operator.ComponentPlan{})
			require.NoError(t, err)
			pod := renderedNodeAgent(t, resources).Spec.Template.Spec
			assert.Nil(t, pod.Affinity)
			assert.NotContains(t, pod.NodeSelector, "nvidia.com/gpu.present")
			if managed {
				assert.Equal(t, "true", pod.NodeSelector[manifests.ClientDeployLabel])
				assert.Len(t, pod.InitContainers, 1)
			} else {
				assert.NotContains(t, pod.NodeSelector, manifests.ClientDeployLabel)
				assert.Empty(t, pod.InitContainers)
			}
		})
	}
}

func TestNvidiaRendererUsesExternalExporterTarget(t *testing.T) {
	for _, namespace := range []string{"", "vendor-metrics"} {
		t.Run(namespace, func(t *testing.T) {
			profile, err := (&plugin.GPUAcceleratorPlugin{}).GetAcceleratorProfile(context.Background())
			require.NoError(t, err)
			profile.ExternalMetricsTarget.Namespace = namespace
			profile.ExternalMetricsTarget.Port = 9401
			profile.ExternalMetricsTarget.MetricsPath = "/vendor-metrics"
			renderer := operator.NewNvidiaResourceRenderer(nodeAgentProfiles{profile: profile})
			resources, err := renderer.Render(context.Background(), &operator.OperatorContext{
				Cluster: &v1.Cluster{Metadata: &v1.Metadata{Name: "cluster-a", Workspace: "workspace-a"}, Spec: &v1.ClusterSpec{
					Version: "v1.2.0", Config: &v1.ClusterConfig{Metrics: &v1.ClusterMetricsConfig{
						AcceleratorExporter: &v1.ClusterAcceleratorExporterConfig{Mode: v1.ClusterAcceleratorExporterModeExternal},
					}},
				}},
				Client: fake.NewClientBuilder().Build(), Namespace: "neutree-system",
			}, operator.ProviderState{}, operator.ComponentPlan{})
			require.NoError(t, err)
			assert.Len(t, resources.Items, 4, "external exporter resources remain owned by their vendor")
			for _, object := range resources.Items {
				assert.NotEqual(t, "ConfigMap", object.GetKind())
				assert.NotEqual(t, manifests.ExporterName, object.GetName())
			}
			container := renderedNodeAgent(t, resources).Spec.Template.Spec.Containers[0]
			assert.Contains(t, container.Args, "--accelerator-exporter-port=9401")
			assert.Contains(t, container.Args, "--accelerator-exporter-metrics-path=/vendor-metrics")
			env := literalEnv(container.Env)
			assert.Equal(t, namespace, env[v1.AcceleratorExporterNamespaceEnvKey], "empty namespace must preserve discovery across namespaces")
			var selector map[string]string
			require.NoError(t, json.Unmarshal([]byte(env[v1.AcceleratorExporterPodSelectorEnvKey]), &selector))
			assert.Equal(t, profile.ExternalMetricsTarget.PodSelector, selector)
			assert.NotContains(t, selector, manifests.OwnerLabel)
		})
	}
}

type nodeAgentProfiles struct{ profile *v1.AcceleratorProfile }

func (p nodeAgentProfiles) SupportPlugins() []string                          { return []string{"nvidia_gpu"} }
func (p nodeAgentProfiles) GetPlugin(string) (publicaccelerator.Plugin, bool) { return nil, false }
func (p nodeAgentProfiles) GetAcceleratorProfile(context.Context, string) (*v1.AcceleratorProfile, error) {
	return p.profile, nil
}

func renderedNodeAgent(t *testing.T, resources *unstructured.UnstructuredList) *appsv1.DaemonSet {
	t.Helper()
	ds := &appsv1.DaemonSet{}
	require.NoError(t, runtime.DefaultUnstructuredConverter.FromUnstructured(renderedObject(t, resources, "DaemonSet", manifests.NodeAgentName), ds))
	return ds
}

func renderedObject(t *testing.T, resources *unstructured.UnstructuredList, kind, name string) map[string]interface{} {
	t.Helper()
	for _, object := range resources.Items {
		if object.GetKind() == kind && object.GetName() == name {
			return object.Object
		}
	}
	t.Fatalf("missing rendered %s/%s", kind, name)
	return nil
}

func literalEnv(env []corev1.EnvVar) map[string]string {
	values := map[string]string{}
	for _, variable := range env {
		if variable.ValueFrom == nil {
			values[variable.Name] = variable.Value
		}
	}
	return values
}
