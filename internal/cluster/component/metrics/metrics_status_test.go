package metrics

import (
	"context"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.openly.dev/pointy"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/neutree-ai/neutree/api/v1"
	acceleratormocks "github.com/neutree-ai/neutree/internal/accelerator/mocks"
	"github.com/neutree-ai/neutree/internal/accelerator/operator"
	publicaccelerator "github.com/neutree-ai/neutree/pkg/accelerator"
)

func readyMetricsDeployment(name string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  "default",
			Generation: 2,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: pointy.Int32(1),
		},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 2,
			ReadyReplicas:      1,
			AvailableReplicas:  1,
			UpdatedReplicas:    1,
			Replicas:           1,
			Conditions: []appsv1.DeploymentCondition{
				{
					Type:   appsv1.DeploymentAvailable,
					Status: corev1.ConditionTrue,
				},
				{
					Type:   appsv1.DeploymentProgressing,
					Status: corev1.ConditionTrue,
				},
			},
		},
	}
}

func readyMetricsDaemonSet(name string, desired int32) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Status: appsv1.DaemonSetStatus{
			DesiredNumberScheduled: desired,
			NumberReady:            desired,
			UpdatedNumberScheduled: desired,
			NumberAvailable:        desired,
		},
	}
}

func Test_checkDeploymentStatus(t *testing.T) {
	tests := []struct {
		name          string
		deployment    *appsv1.Deployment
		expectedReady bool
		expectError   bool
	}{
		{
			name: "Deployment ready and available",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "vmagent",
					Namespace:  "default",
					Generation: 2,
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: pointy.Int32(1),
				},
				Status: appsv1.DeploymentStatus{
					ObservedGeneration: 2,
					ReadyReplicas:      1,
					AvailableReplicas:  1,
					UpdatedReplicas:    1,
					Replicas:           1,
					Conditions: []appsv1.DeploymentCondition{
						{
							Type:   appsv1.DeploymentAvailable,
							Status: corev1.ConditionTrue,
						},
						{
							Type:   appsv1.DeploymentProgressing,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			expectedReady: true,
			expectError:   false,
		},
		{
			name: "Deployment not ready",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "vmagent",
					Namespace:  "default",
					Generation: 3,
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: pointy.Int32(1),
				},
				Status: appsv1.DeploymentStatus{
					ObservedGeneration: 2,
					ReadyReplicas:      1,
					AvailableReplicas:  1,
					UpdatedReplicas:    1,
					Replicas:           1,
					Conditions: []appsv1.DeploymentCondition{
						{
							Type:   appsv1.DeploymentAvailable,
							Status: corev1.ConditionTrue,
						},
						{
							Type:   appsv1.DeploymentProgressing,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			expectedReady: false,
			expectError:   false,
		},
		{
			name: "Deployment not found",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "vmagent-test",
					Namespace: "default",
				},
				Status: appsv1.DeploymentStatus{
					ReadyReplicas:     1,
					AvailableReplicas: 3,
				},
			},
			expectedReady: false,
			expectError:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup fake client with the deployment
			fakeClient := fake.NewClientBuilder().
				WithObjects(tt.deployment).
				Build()

			metricsCmpt := &MetricsComponent{
				ctrlClient: fakeClient,
				namespace:  "default",
				cluster: &v1.Cluster{
					Metadata: &v1.Metadata{Name: "test", Workspace: "default"},
					Spec:     &v1.ClusterSpec{},
				},
			}

			ready, _, _, err := metricsCmpt.checkDeploymentStatus(context.Background())
			if tt.expectError {
				assert.Error(t, err, "Expected error but got none")
			} else {
				assert.NoError(t, err, "Did not expect error but got one")
				assert.Equal(t, tt.expectedReady, ready, "Deployment readiness mismatch")
			}
		})
	}
}

func TestCheckResourcesStatusIncludesKubeStateMetrics(t *testing.T) {
	fakeClient := fake.NewClientBuilder().
		WithObjects(
			readyMetricsDeployment("vmagent"),
			readyMetricsDeployment("neutree-kube-state-metrics"),
			readyMetricsDaemonSet("neutree-node-exporter", 2),
			readyMetricsDaemonSet("neutree-node-agent", 2),
		).
		Build()

	metricsCmpt := &MetricsComponent{
		ctrlClient:            fakeClient,
		namespace:             "default",
		metricsRemoteWriteURL: "https://metrics.example.com/api/v1/write",
		cluster: &v1.Cluster{
			Metadata: &v1.Metadata{Name: "test", Workspace: "default"},
			Spec:     &v1.ClusterSpec{Version: "v1.1.0"},
		},
	}

	status, err := metricsCmpt.CheckResourcesStatus(context.Background())

	assert.NoError(t, err)
	assert.True(t, status.Ready())
	assert.True(t, status.DeploymentReady)
	assert.True(t, status.KubeStateMetricsRequired)
	assert.True(t, status.KubeStateMetricsDeploymentReady)
	assert.True(t, status.NodeExporterDaemonSetReady)
	assert.True(t, status.NeutreeNodeAgentMetricsRequired)
	assert.True(t, status.NeutreeNodeAgentMetricsDaemonSetReady)
	assert.Equal(t, 1, status.PodsReady)
	assert.Equal(t, 1, status.KubeStateMetricsPodsReady)
}

func TestCheckResourcesStatusChecksPlannedNodeAgents(t *testing.T) {
	for _, test := range []struct {
		name                          string
		operator, managed, missingCPU bool
		gpuDesired, cpuDesired        int32
		cpuNotReady, expectedReady    bool
	}{
		{name: "single legacy agent", gpuDesired: 2, expectedReady: true},
		{name: "operator with OS dependencies", operator: true, gpuDesired: 1, cpuDesired: 1, expectedReady: true},
		{name: "managed mixed nodes", operator: true, managed: true, gpuDesired: 1, cpuDesired: 1, expectedReady: true},
		{name: "managed GPU only", operator: true, managed: true, gpuDesired: 1, expectedReady: true},
		{name: "GPU recovery retains CPU coverage", operator: true, managed: true, cpuDesired: 1, expectedReady: true},
		{name: "CPU agent missing", operator: true, managed: true, gpuDesired: 1, missingCPU: true},
		{name: "CPU agent not ready", operator: true, managed: true, gpuDesired: 1, cpuDesired: 1, cpuNotReady: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			objects := []client.Object{
				readyMetricsDeployment("neutree-kube-state-metrics"),
				readyMetricsDaemonSet(nodeExporterDaemonSetName, test.gpuDesired+test.cpuDesired),
				readyMetricsDaemonSet(neutreeNodeAgentMetricsName, test.gpuDesired),
			}
			component := &MetricsComponent{
				namespace: "default",
				cluster: &v1.Cluster{
					Metadata: &v1.Metadata{Name: "test", Workspace: "default"},
					Spec:     &v1.ClusterSpec{Version: "v1.2.0"},
				},
			}
			if test.operator {
				component.WithAcceleratorOperators(map[string]publicaccelerator.AcceleratorOperator{publicaccelerator.NvidiaAcceleratorType: operator.NewNvidiaAcceleratorOperator()})
				objects = append(objects, metricsTestNode("gpu", map[string]string{
					publicaccelerator.NvidiaGPUDiscoveryLabelKey: publicaccelerator.NvidiaGPUDiscoveryLabelValue,
				}), readyMetricsDaemonSet("nvidia-gpu-dcgm-exporter", test.gpuDesired))
				if !test.missingCPU {
					cpu := readyMetricsDaemonSet(cpuNodeAgentMetricsName, test.cpuDesired)
					if test.cpuNotReady {
						cpu.Status.NumberReady = 0
					}
					objects = append(objects, cpu)
				}
				manager := acceleratormocks.NewMockManager(t)
				manager.On("SupportPlugins").Return([]string{v1.AcceleratorTypeNVIDIAGPU.String()})
				manager.On("GetAcceleratorProfile", mock.Anything, metricsProfileInput(v1.AcceleratorTypeNVIDIAGPU.String())).Return(metricsNvidiaProfile(test.managed), nil)
				component.acceleratorMgr = manager
			}
			component.ctrlClient = fake.NewClientBuilder().WithObjects(objects...).Build()

			status, err := component.CheckResourcesStatus(context.Background())
			require.NoError(t, err)
			assert.True(t, status.NeutreeNodeAgentMetricsRequired)
			assert.Equal(t, test.expectedReady, status.NeutreeNodeAgentMetricsDaemonSetReady)
			expectedPods := int(test.gpuDesired + test.cpuDesired)
			expectedTotal := expectedPods
			if test.missingCPU || test.cpuNotReady {
				expectedPods -= int(test.cpuDesired)
			}
			if test.missingCPU {
				expectedTotal -= int(test.cpuDesired)
			}
			assert.Equal(t, expectedPods, status.NeutreeNodeAgentMetricsPodsReady)
			assert.Equal(t, expectedTotal, status.NeutreeNodeAgentMetricsTotalPods)
			if test.missingCPU {
				assert.Contains(t, status.Errors[0], cpuNodeAgentMetricsName)
			}
		})
	}
}

func TestCheckResourcesStatusDoesNotRequireVMAgentWithoutValidRemoteWrite(t *testing.T) {
	for _, remoteWriteURL := range []string{"", "invalid-url"} {
		t.Run("remote write URL="+remoteWriteURL, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().
				WithObjects(
					readyMetricsDeployment("neutree-kube-state-metrics"),
					readyMetricsDaemonSet("neutree-node-exporter", 2),
					readyMetricsDaemonSet("neutree-node-agent", 2),
				).
				Build()

			metricsCmpt := &MetricsComponent{
				ctrlClient:            fakeClient,
				namespace:             "default",
				metricsRemoteWriteURL: remoteWriteURL,
				cluster: &v1.Cluster{
					Metadata: &v1.Metadata{Name: "test", Workspace: "default"},
					Spec:     &v1.ClusterSpec{Version: "v1.1.0"},
				},
			}

			status, err := metricsCmpt.CheckResourcesStatus(context.Background())

			assert.NoError(t, err)
			assert.True(t, status.Ready())
			assert.False(t, status.VMAgentRequired)
			assert.False(t, status.DeploymentReady)
			assert.Empty(t, status.Errors)
		})
	}
}

func TestCheckResourcesStatusSkipsKubeStateMetricsBeforeV110(t *testing.T) {
	fakeClient := fake.NewClientBuilder().
		WithObjects(
			readyMetricsDeployment("vmagent"),
			readyMetricsDaemonSet("neutree-node-exporter", 2),
		).
		Build()

	metricsCmpt := &MetricsComponent{
		ctrlClient:            fakeClient,
		namespace:             "default",
		metricsRemoteWriteURL: "https://metrics.example.com/api/v1/write",
		cluster: &v1.Cluster{
			Metadata: &v1.Metadata{Name: "test", Workspace: "default"},
			Spec:     &v1.ClusterSpec{Version: "v1.0.0"},
		},
	}

	status, err := metricsCmpt.CheckResourcesStatus(context.Background())

	assert.NoError(t, err)
	assert.True(t, status.Ready())
	assert.True(t, status.DeploymentReady)
	assert.False(t, status.KubeStateMetricsRequired)
	assert.False(t, status.KubeStateMetricsDeploymentReady)
	assert.False(t, status.NodeExporterRequired)
	assert.False(t, status.NodeExporterDaemonSetReady)
}

func TestCheckResourcesStatusIncludesAcceleratorExporterDaemonSet(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fakeClient := fake.NewClientBuilder().
		WithObjects(
			readyMetricsDeployment("vmagent"),
			readyMetricsDeployment("neutree-kube-state-metrics"),
			readyMetricsDaemonSet("neutree-node-exporter", 1),
			readyMetricsDaemonSet("neutree-node-agent", 1),
			readyMetricsDaemonSet("nvidia-gpu-dcgm-exporter", 1),
			&corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "gpu-node",
					Labels: map[string]string{
						"nvidia.com/gpu.present": "true",
					},
				},
			},
		).
		Build()

	metricsCmpt := &MetricsComponent{
		ctrlClient:            fakeClient,
		namespace:             "default",
		metricsRemoteWriteURL: "https://metrics.example.com/api/v1/write",
		acceleratorMgr:        metricsTestAcceleratorManager(t),
		cluster: &v1.Cluster{
			Metadata: &v1.Metadata{Name: "test", Workspace: "default"},
			Spec:     &v1.ClusterSpec{Version: "v1.1.0"},
		},
	}

	status, err := metricsCmpt.CheckResourcesStatus(context.Background())

	assert.NoError(t, err)
	assert.True(t, status.Ready())
	assert.True(t, status.NodeExporterDaemonSetReady)
	assert.True(t, status.NeutreeNodeAgentMetricsDaemonSetReady)
	assert.True(t, status.AcceleratorExporterRequired)
	assert.True(t, status.AcceleratorExporterDaemonSetsReady)
}

func TestCheckResourcesStatusDoesNotCheckExternalExporterDaemonSet(t *testing.T) {
	fakeClient := fake.NewClientBuilder().
		WithObjects(
			readyMetricsDeployment("vmagent"),
			readyMetricsDeployment("neutree-kube-state-metrics"),
			readyMetricsDaemonSet("neutree-node-exporter", 1),
			readyMetricsDaemonSet("neutree-node-agent", 1),
		).
		Build()

	metricsCmpt := &MetricsComponent{
		ctrlClient:            fakeClient,
		namespace:             "default",
		metricsRemoteWriteURL: "https://metrics.example.com/api/v1/write",
		cluster: &v1.Cluster{
			Metadata: &v1.Metadata{Name: "test", Workspace: "default"},
			Spec: &v1.ClusterSpec{
				Version: "v1.1.2",
				Config: &v1.ClusterConfig{Metrics: &v1.ClusterMetricsConfig{
					AcceleratorExporter: &v1.ClusterAcceleratorExporterConfig{
						Mode: v1.ClusterAcceleratorExporterModeExternal,
					},
				}},
			},
		},
	}

	status, err := metricsCmpt.CheckResourcesStatus(context.Background())

	assert.NoError(t, err)
	assert.True(t, status.Ready())
	assert.False(t, status.AcceleratorExporterRequired)
	assert.False(t, status.AcceleratorExporterDaemonSetsReady)
	assert.Empty(t, status.Errors)
}
