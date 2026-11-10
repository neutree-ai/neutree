package operator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
	publicaccelerator "github.com/neutree-ai/neutree/pkg/accelerator"
)

type runtimeClassProfiles struct{}

func (runtimeClassProfiles) SupportPlugins() []string { return []string{NVIDIAAcceleratorType} }
func (runtimeClassProfiles) GetPlugin(string) (publicaccelerator.Plugin, bool) {
	return runtimeClassVirtualization{}, true
}
func (runtimeClassProfiles) GetAcceleratorProfile(ctx context.Context, _ string) (*v1.AcceleratorProfile, error) {
	return &v1.AcceleratorProfile{
		NodeAgentRuntime: &v1.NodeAgentRuntimeProfile{Env: map[string]string{"NVIDIA_VISIBLE_DEVICES": "all"}},
		MetricsExporter:  &v1.AcceleratorExporterProfile{Name: "dcgm-exporter", Port: 19400, Env: map[string]string{"NVIDIA_VISIBLE_DEVICES": "all"}},
	}, nil
}

type runtimeClassVirtualization struct{}

func (runtimeClassVirtualization) Handle() publicaccelerator.PluginHandle { return nil }
func (runtimeClassVirtualization) Resource() string                       { return NVIDIAAcceleratorType }
func (runtimeClassVirtualization) Type() string                           { return "internal" }
func (runtimeClassVirtualization) ResolveClusterVirtualizationConfig(context.Context, *v1.Cluster) (*publicaccelerator.VirtualizationConfig, error) {
	return &publicaccelerator.VirtualizationConfig{Supported: true, ConfigPatch: map[string]interface{}{
		"devicePlugin": map[string]interface{}{
			"nvidiaNodeSelector": map[string]interface{}{"neutree.ai/nvidia-vgpu-enabled": "true"},
		},
	}}, nil
}

func TestNvidiaManagedClientsUsePolicyRuntimeClassBeforeSandboxCreation(t *testing.T) {
	tests := []struct {
		name, policyClass, toolkitEnv, runtimeVersion, want string
		managed, virtualization, mixed                      bool
	}{
		{name: "managed default", managed: true, runtimeVersion: "containerd://1.7.28", want: "nvidia"},
		{name: "custom policy class", managed: true, runtimeVersion: "containerd://1.7.28", policyClass: "custom-nvidia", want: "custom-nvidia"},
		{name: "toolkit env cannot override policy", managed: true, runtimeVersion: "containerd://1.7.28", policyClass: "custom-nvidia", toolkitEnv: "wrong-handler", want: "custom-nvidia"},
		{name: "toolkit env cannot override default", managed: true, runtimeVersion: "containerd://1.7.28", toolkitEnv: "wrong-handler", want: "nvidia"},
		{name: "managed HAMi clients", managed: true, runtimeVersion: "containerd://1.7.28", policyClass: "custom-nvidia", virtualization: true, want: "custom-nvidia"},
		{name: "OS provider unchanged", runtimeVersion: "containerd://1.7.28", policyClass: "custom-nvidia"},
		{name: "CRI-O unchanged", managed: true, runtimeVersion: "cri-o://1.31.0"},
		{name: "Docker unchanged", managed: true, runtimeVersion: "docker://24.0.0"},
		{name: "unknown runtime unchanged", managed: true},
		{name: "mixed runtime unchanged", managed: true, runtimeVersion: "containerd://1.7.28", mixed: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			ctrl := newNvidiaProviderClient(t, true)
			node := &corev1.Node{}
			require.NoError(t, ctrl.Get(ctx, client.ObjectKey{Name: "gpu-node"}, node))
			node.Status.NodeInfo.ContainerRuntimeVersion = test.runtimeVersion
			require.NoError(t, ctrl.Status().Update(ctx, node))
			if test.mixed {
				require.NoError(t, ctrl.Create(ctx, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "second-gpu", Labels: map[string]string{NVIDIAGPUNodeLabel: NVIDIAGPUNodeValue}}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{ContainerRuntimeVersion: "cri-o://1.31.0"}}}))
			}
			policy := &unstructured.Unstructured{}
			policy.SetGroupVersionKind(NVIDIAClusterPolicyGVK)
			require.NoError(t, ctrl.Get(ctx, client.ObjectKey{Name: NVIDIAClusterPolicyName}, policy))
			require.NoError(t, unstructured.SetNestedField(policy.Object, test.policyClass, "spec", "operator", "runtimeClass"))
			require.NoError(t, unstructured.SetNestedField(policy.Object, "docker", "spec", "operator", "defaultRuntime"))
			require.NoError(t, unstructured.SetNestedField(policy.Object, test.managed, "spec", "driver", "enabled"))
			if test.toolkitEnv != "" {
				require.NoError(t, unstructured.SetNestedSlice(policy.Object, []interface{}{map[string]interface{}{"name": "CONTAINERD_RUNTIME_CLASS", "value": test.toolkitEnv}}, "spec", "toolkit", "env"))
			}
			require.NoError(t, ctrl.Update(ctx, policy))
			state, err := NewNvidiaProvider(ctrl).Evaluate(ctx)
			require.NoError(t, err)
			opCtx := lifecycleContext(ctrl)
			opCtx.Cluster.Spec.Version = "v1.2.1"
			opCtx.Cluster.Spec.AcceleratorVirtualization = &v1.AcceleratorVirtualizationSpec{Enabled: test.virtualization}
			// No RuntimeClass exists in this API: the manifest must still request it rather than use the default runtime.
			resources, err := NewNvidiaResourceRenderer(runtimeClassProfiles{}).Render(ctx, opCtx, state, BuildNvidiaComponentPlan(state))
			require.NoError(t, err)
			workloads, controlPlane, devicePlugins := 0, 0, 0
			for _, object := range resources.Items {
				switch object.GetKind() {
				case "DaemonSet":
					workloads++
					runtimeClass, found, err := unstructured.NestedString(object.Object, "spec", "template", "spec", "runtimeClassName")
					require.NoError(t, err)
					require.Equal(t, test.want, runtimeClass, object.GetName())
					require.Equal(t, test.want != "", found, object.GetName())
					if object.GetLabels()["app.kubernetes.io/component"] == "hami-device-plugin" {
						devicePlugins++
					}
				case "Deployment":
					controlPlane++
					_, found, err := unstructured.NestedString(object.Object, "spec", "template", "spec", "runtimeClassName")
					require.NoError(t, err)
					require.False(t, found, "HAMi control plane must retain its runtime selection")
				}
			}
			require.GreaterOrEqual(t, workloads, 2, "NodeAgent and DCGM must both be checked")
			if test.virtualization {
				require.Positive(t, devicePlugins)
				require.Zero(t, controlPlane, "HAMiComponent owns the control plane")
			}
		})
	}
}

func TestNvidiaUnknownContainerRuntimeIsExplainedInStatus(t *testing.T) {
	ctrl := newNvidiaProviderClient(t, true)
	opCtx := lifecycleContext(ctrl)
	saveLifecycleResources(t, opCtx, lifecycleDaemonSet("neutree-node-agent"), lifecycleDaemonSet("nvidia-gpu-dcgm-exporter"))
	status, err := NewNvidiaAcceleratorOperator(nil).Status(context.Background(), opCtx)
	require.NoError(t, err)
	require.Equal(t, v1.ComponentPhaseReady, status.Phase)
	require.Contains(t, status.Message, "GPU container runtime is unknown")
	require.Contains(t, status.Message, "explicit NVIDIA RuntimeClass selection is not enabled")
}
