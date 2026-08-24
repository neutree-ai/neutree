package metrics

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/neutree-ai/neutree/api/v1"
	acceleratormocks "github.com/neutree-ai/neutree/internal/accelerator/mocks"
)

func TestMetricsResourcesProjectsSelectedRuntimeToSingleNodeAgent(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "npu-node",
			Labels: map[string]string{"example.com/accelerator": "present"},
		},
	}).Build()
	profile := &v1.AcceleratorProfile{
		AcceleratorType: "ascend_npu",
		NodeAgentRuntime: &v1.NodeAgentRuntimeProfile{
			Privileged:   true,
			Capabilities: &v1.NodeAgentRuntimeCapabilities{Add: []string{"SYS_ADMIN"}},
			Volumes: []v1.ComponentVolume{{
				Name:     "ascend-driver",
				HostPath: &v1.ComponentHostPathVolumeSource{Path: "/opt/ascend/driver", Type: v1.ComponentHostPathTypeDirectory},
			}},
			VolumeMounts: []v1.ComponentVolumeMount{{Name: "ascend-driver", MountPath: "/opt/ascend/driver"}},
		},
		MetricsExporter: &v1.AcceleratorExporterProfile{
			Name:  "npu-exporter",
			Image: "registry.example/npu-exporter:v1.0.0",
			Port:  8082,
			Runtime: &v1.AcceleratorExporterRuntimeProfile{
				NodeSelector: map[string]string{"example.com/accelerator": "present"},
			},
		},
	}
	acceleratorMgr := acceleratormocks.NewMockManager(t)
	acceleratorMgr.EXPECT().SupportPlugins().Return([]string{"ascend_npu"}).Maybe()
	acceleratorMgr.EXPECT().GetAcceleratorProfile(mock.Anything, "ascend_npu").Return(profile, nil).Maybe()

	component := &MetricsComponent{
		cluster: &v1.Cluster{
			Metadata: &v1.Metadata{Name: "test-cluster", Workspace: "test-workspace"},
			Spec:     &v1.ClusterSpec{Version: "v1.1.0"},
		},
		namespace:       "test-namespace",
		imagePullSecret: "pull-secret",
		ctrlClient:      client,
		acceleratorMgr:  acceleratorMgr,
	}

	objects, err := component.GetMetricsResources(context.Background())
	require.NoError(t, err)

	nodeAgent := findMetricsDaemonSet(t, objects, neutreeNodeAgentMetricsName)
	container := nodeAgent.Spec.Template.Spec.Containers[0]

	assert.Contains(t, container.Args, "--accelerator-type=ascend_npu")
	assert.Nil(t, nodeAgent.Spec.Template.Spec.Affinity)
	require.NotNil(t, container.SecurityContext)
	assert.True(t, *container.SecurityContext.Privileged)
	assert.Contains(t, container.SecurityContext.Capabilities.Add, corev1.Capability("SYS_ADMIN"))
	assert.Contains(t, daemonSetHostPathNames(nodeAgent), "ascend-driver")
	assert.False(t, hasMetricsDaemonSet(objects, neutreeNodeAgentMetricsName+"-ascend-npu"))
}

func TestCheckResourcesStatusChecksSingleNodeAgent(t *testing.T) {
	client := fake.NewClientBuilder().WithObjects(
		readyMetricsDeployment("neutree-kube-state-metrics"),
		readyMetricsDaemonSet(nodeExporterDaemonSetName, 2),
		readyMetricsDaemonSet(neutreeNodeAgentMetricsName, 2),
	).Build()
	component := &MetricsComponent{
		ctrlClient: client,
		namespace:  "default",
		cluster: &v1.Cluster{
			Metadata: &v1.Metadata{Name: "test", Workspace: "default"},
			Spec:     &v1.ClusterSpec{Version: "v1.1.0"},
		},
	}

	status, err := component.CheckResourcesStatus(context.Background())

	require.NoError(t, err)
	assert.True(t, status.NeutreeNodeAgentMetricsRequired)
	assert.True(t, status.NeutreeNodeAgentMetricsDaemonSetReady)
	assert.Equal(t, 2, status.NeutreeNodeAgentMetricsPodsReady)
	assert.Equal(t, 2, status.NeutreeNodeAgentMetricsTotalPods)
}

func daemonSetHostPathNames(daemonSet *appsv1.DaemonSet) []string {
	names := make([]string, 0, len(daemonSet.Spec.Template.Spec.Volumes))
	for _, volume := range daemonSet.Spec.Template.Spec.Volumes {
		if volume.HostPath != nil {
			names = append(names, volume.Name)
		}
	}

	return names
}

func hasMetricsDaemonSet(objects *unstructured.UnstructuredList, name string) bool {
	for _, object := range objects.Items {
		if object.GetKind() == "DaemonSet" && object.GetName() == name {
			return true
		}
	}

	return false
}
