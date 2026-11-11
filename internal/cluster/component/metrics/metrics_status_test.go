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
	acceleratoroperator "github.com/neutree-ai/neutree/internal/accelerator/operator"
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
		name                             string
		operator, managed                bool
		gpuDesired, cpuDesired           int32
		missingCPU, missingAccelerator   bool
		cpuNotReady, acceleratorNotReady bool
		expectedReady                    bool
		external, missingExporter        bool
		wantError                        string
	}{
		{name: "single legacy agent", gpuDesired: 2, expectedReady: true},
		{name: "legacy agent not ready", gpuDesired: 2, acceleratorNotReady: true, wantError: "node-agent DaemonSet/neutree-node-agent is not ready (0/2)"},
		{name: "operator with OS dependencies", operator: true, gpuDesired: 1, cpuDesired: 1, expectedReady: true},
		{name: "managed mixed nodes", operator: true, managed: true, gpuDesired: 1, cpuDesired: 1, expectedReady: true},
		{name: "managed exporter missing still blocks Metrics", operator: true, managed: true, gpuDesired: 1, cpuDesired: 1, expectedReady: true, missingExporter: true, wantError: "accelerator-exporter daemonset nvidia-gpu-dcgm-exporter check failed"},
		{name: "external exporter is not owned by Metrics", operator: true, managed: true, external: true, gpuDesired: 1, cpuDesired: 1, expectedReady: true},
		{name: "managed GPU only", operator: true, managed: true, gpuDesired: 1, expectedReady: true},
		{name: "GPU recovery retains CPU coverage", operator: true, managed: true, cpuDesired: 1, expectedReady: true},
		{name: "CPU agent missing", operator: true, managed: true, gpuDesired: 1, cpuDesired: 1, missingCPU: true, wantError: "cpu node-agent DaemonSet/neutree-node-agent-cpu check failed"},
		{name: "CPU agent not ready", operator: true, managed: true, gpuDesired: 1, cpuDesired: 1, cpuNotReady: true, wantError: "cpu node-agent DaemonSet/neutree-node-agent-cpu is not ready (0/1)"},
		{name: "accelerator agent missing", operator: true, managed: true, gpuDesired: 1, cpuDesired: 1, missingAccelerator: true, wantError: "accelerator node-agent DaemonSet/neutree-node-agent check failed"},
		{name: "accelerator agent not ready", operator: true, managed: true, gpuDesired: 1, cpuDesired: 1, acceleratorNotReady: true, wantError: "accelerator node-agent DaemonSet/neutree-node-agent is not ready (0/1)"},
	} {
		t.Run(test.name, func(t *testing.T) {
			objects := []client.Object{
				readyMetricsDeployment("neutree-kube-state-metrics"),
				readyMetricsDaemonSet(nodeExporterDaemonSetName, test.gpuDesired+test.cpuDesired),
			}
			if !test.missingAccelerator {
				agent := readyMetricsDaemonSet(neutreeNodeAgentMetricsName, test.gpuDesired)
				if test.acceleratorNotReady {
					agent.Status.NumberReady = 0
				}
				objects = append(objects, agent)
			}
			component := &MetricsComponent{
				namespace: "default",
				cluster: &v1.Cluster{
					Metadata: &v1.Metadata{Name: "test", Workspace: "default"},
					Spec:     &v1.ClusterSpec{Version: "v1.2.0"},
				},
			}
			if test.operator {
				objects = append(objects, metricsTestNode("gpu", map[string]string{
					publicaccelerator.NvidiaGPUDiscoveryLabelKey: publicaccelerator.NvidiaGPUDiscoveryLabelValue,
				}))
				if !test.external && !test.missingExporter {
					objects = append(objects, readyMetricsDaemonSet("nvidia-gpu-dcgm-exporter", test.gpuDesired))
				}
				if test.cpuDesired > 0 {
					objects = append(objects, metricsTestNode("cpu", nil))
				}
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
				component.WithAcceleratorOperators(map[string]acceleratoroperator.AcceleratorOperator{
					publicaccelerator.NvidiaAcceleratorType: acceleratoroperator.NewNvidiaAcceleratorOperator(),
				})
			}
			component.ctrlClient = fake.NewClientBuilder().WithObjects(objects...).Build()

			if test.external {
				component.cluster.Spec.Config = &v1.ClusterConfig{Metrics: &v1.ClusterMetricsConfig{AcceleratorExporter: &v1.ClusterAcceleratorExporterConfig{Mode: v1.ClusterAcceleratorExporterModeExternal}}}
			}
			status, err := component.CheckResourcesStatus(context.Background())
			require.NoError(t, err)
			assert.True(t, status.NeutreeNodeAgentMetricsRequired)
			assert.Equal(t, test.expectedReady, status.NeutreeNodeAgentMetricsDaemonSetReady)
			assert.Equal(t, test.expectedReady && !test.missingExporter, status.Ready())
			assert.Equal(t, test.operator && !test.external, status.AcceleratorExporterRequired)
			if test.wantError != "" {
				assert.Contains(t, status.String(), test.wantError)
			} else {
				assert.Empty(t, status.Errors)
			}
			expectedPods := int(test.gpuDesired + test.cpuDesired)
			expectedTotal := expectedPods
			if test.missingCPU || test.cpuNotReady {
				expectedPods -= int(test.cpuDesired)
			}
			if test.missingCPU {
				expectedTotal -= int(test.cpuDesired)
			}
			if test.missingAccelerator || test.acceleratorNotReady {
				expectedPods -= int(test.gpuDesired)
			}
			if test.missingAccelerator {
				expectedTotal -= int(test.gpuDesired)
			}
			assert.Equal(t, expectedPods, status.NeutreeNodeAgentMetricsPodsReady)
			assert.Equal(t, expectedTotal, status.NeutreeNodeAgentMetricsTotalPods)
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
