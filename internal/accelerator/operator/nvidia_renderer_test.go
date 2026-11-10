package operator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/neutree-ai/neutree/api/v1"
	publicaccelerator "github.com/neutree-ai/neutree/pkg/accelerator"
)

func TestNvidiaRendererChecksHAMiPreflightBeforeRendering(t *testing.T) {
	for _, test := range []struct {
		name            string
		webhookLabels   map[string]string
		blockingReasons []string
		wantError       string
	}{
		{name: "first installation"},
		{name: "existing owner", webhookLabels: map[string]string{
			"neutree.ai/component": "hami", v1.NeutreeClusterLabelKey: "cluster-a", v1.NeutreeClusterWorkspaceLabelKey: "workspace-a",
		}},
		{name: "NVIDIA device plugin still enabled", blockingReasons: []string{
			"NVIDIA GPU Operator devicePlugin is enabled; disable it before enabling HAMi NVIDIA vGPU",
		}, wantError: "accelerator plugin blocked HAMi virtualization: NVIDIA GPU Operator devicePlugin is enabled; disable it before enabling HAMi NVIDIA vGPU"},
	} {
		t.Run(test.name, func(t *testing.T) {
			builder := fake.NewClientBuilder()
			if test.webhookLabels != nil {
				builder = builder.WithObjects(&admissionregistrationv1.MutatingWebhookConfiguration{
					ObjectMeta: metav1.ObjectMeta{Name: "hami-webhook", Labels: test.webhookLabels},
				})
			}
			profile := hamiRendererProfiles{config: &publicaccelerator.VirtualizationConfig{
				Supported: true, CandidateNodes: []string{"gpu-node"}, BlockingReasons: test.blockingReasons,
				ConfigPatch: map[string]interface{}{"devicePlugin": map[string]interface{}{
					"nvidiaNodeSelector": map[string]interface{}{"neutree.ai/nvidia-vgpu-enabled": "true"},
				}},
			}}
			opCtx := &OperatorContext{
				Cluster: &v1.Cluster{Metadata: &v1.Metadata{Name: "cluster-a", Workspace: "workspace-a"}, Spec: &v1.ClusterSpec{
					Version: "v1.2.1", AcceleratorVirtualization: &v1.AcceleratorVirtualizationSpec{Enabled: true},
				}},
				Client: builder.Build(), Namespace: "neutree-system",
			}
			resources, err := NewNvidiaResourceRenderer(profile).Render(context.Background(), opCtx, ProviderState{}, ComponentPlan{})
			if test.wantError != "" {
				require.EqualError(t, err, test.wantError)
				require.Nil(t, resources, "failed preflight must not produce resources for application")
				return
			}
			require.NoError(t, err)
			found := false
			for _, object := range resources.Items {
				if object.GetKind() == daemonSetKind && object.GetName() == "hami-device-plugin" {
					found = true
				}
			}
			require.True(t, found, "valid HAMi ownership must allow device-plugin rendering")
		})
	}
}

type hamiRendererProfiles struct {
	config *publicaccelerator.VirtualizationConfig
}

func (p hamiRendererProfiles) SupportPlugins() []string                          { return []string{NVIDIAAcceleratorType} }
func (p hamiRendererProfiles) GetPlugin(string) (publicaccelerator.Plugin, bool) { return p, true }
func (hamiRendererProfiles) GetAcceleratorProfile(context.Context, string) (*v1.AcceleratorProfile, error) {
	return &v1.AcceleratorProfile{MetricsExporter: &v1.AcceleratorExporterProfile{Name: "dcgm-exporter", Port: 19400}}, nil
}
func (hamiRendererProfiles) Handle() publicaccelerator.PluginHandle { return nil }
func (hamiRendererProfiles) Resource() string                       { return NVIDIAAcceleratorType }
func (hamiRendererProfiles) Type() string                           { return "internal" }
func (p hamiRendererProfiles) ResolveClusterVirtualizationConfig(context.Context, *v1.Cluster) (*publicaccelerator.VirtualizationConfig, error) {
	return p.config, nil
}
