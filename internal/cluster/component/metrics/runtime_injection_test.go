package metrics

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/neutree-ai/neutree/api/v1"
	acceleratormocks "github.com/neutree-ai/neutree/internal/accelerator/mocks"
	"github.com/stretchr/testify/mock"
)

func TestMetricsRendersProfileInitContainersAndRuntime(t *testing.T) {
	manager := acceleratormocks.NewMockManager(t)
	manager.On("SupportPlugins").Return([]string{v1.AcceleratorTypeNVIDIAGPU.String()})
	manager.On("GetAcceleratorProfile", mock.Anything, v1.AcceleratorTypeNVIDIAGPU.String()).Return(&v1.AcceleratorProfile{
		AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.String(),
		MetricsExporter: &v1.AcceleratorExporterProfile{
			Name: "dcgm-exporter", Image: "example/dcgm:v1", Port: 19400,
			Runtime: &v1.AcceleratorExporterRuntimeProfile{
				Runtime:        "nvidia",
				InitContainers: []corev1.Container{{Name: "toolkit-validation", Image: "busybox:1.36"}},
			},
		},
		NodeAgentRuntime: &v1.NodeAgentRuntimeProfile{
			Runtime:        "nvidia",
			InitContainers: []corev1.Container{{Name: "toolkit-validation", Image: "busybox:1.36"}},
		},
	}, nil)

	component := &MetricsComponent{
		cluster:   &v1.Cluster{Metadata: &v1.Metadata{Name: "cluster", Workspace: "workspace"}, Spec: &v1.ClusterSpec{Version: "v1.2.0"}},
		namespace: "neutree-system", imagePullSecret: "pull-secret", metricsRemoteWriteURL: "https://metrics.example/write",
		acceleratorMgr: manager, ctrlClient: fake.NewClientBuilder().WithObjects(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "gpu", Labels: map[string]string{"nvidia.com/gpu.present": "true"}}}).Build(),
	}

	resources, err := component.GetMetricsResources(context.Background())
	require.NoError(t, err)

	for _, name := range []string{"neutree-node-agent", "nvidia-gpu-dcgm-exporter"} {
		daemonSet := &appsv1.DaemonSet{}
		for _, object := range resources.Items {
			if object.GetKind() == "DaemonSet" && object.GetName() == name {
				require.NoError(t, runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, daemonSet))
			}
		}
		require.NotEmpty(t, daemonSet.Name, name)
		require.NotNil(t, daemonSet.Spec.Template.Spec.RuntimeClassName, name)
		require.Equal(t, "nvidia", *daemonSet.Spec.Template.Spec.RuntimeClassName, name)
		require.Len(t, daemonSet.Spec.Template.Spec.InitContainers, 1, name)
	}
}
