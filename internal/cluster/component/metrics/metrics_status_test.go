package metrics

import (
	"context"
	"testing"

	"github.com/gin-gonic/gin"
	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator"
	"github.com/stretchr/testify/assert"
	"go.openly.dev/pointy"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
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
				ctrlClient:            fakeClient,
				namespace:             "default",
				metricsRemoteWriteURL: "http://example.com/write",
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
		metricsRemoteWriteURL: "http://example.com/write",
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
		metricsRemoteWriteURL: "http://example.com/write",
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

func TestCheckResourcesStatusRequiresLocalMetricsComponentsWithoutRemoteWrite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fakeClient := fake.NewClientBuilder().
		WithObjects(
			readyMetricsDaemonSet("neutree-node-agent", 1),
			readyMetricsDaemonSet("neutree-node-exporter", 1),
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
		ctrlClient:     fakeClient,
		namespace:      "default",
		acceleratorMgr: accelerator.NewManager(gin.New()),
		cluster: &v1.Cluster{
			Metadata: &v1.Metadata{Name: "test", Workspace: "default"},
			Spec:     &v1.ClusterSpec{Version: "v1.1.0"},
		},
	}

	status, err := metricsCmpt.CheckResourcesStatus(context.Background())

	assert.NoError(t, err)
	assert.True(t, status.Ready())
	assert.False(t, status.DeploymentRequired)
	assert.False(t, status.DeploymentReady)
	assert.True(t, status.NodeExporterRequired)
	assert.True(t, status.NodeExporterDaemonSetReady)
	assert.True(t, status.NeutreeNodeAgentMetricsRequired)
	assert.True(t, status.NeutreeNodeAgentMetricsDaemonSetReady)
	assert.False(t, status.KubeStateMetricsRequired)
	assert.True(t, status.AcceleratorExporterRequired)
	assert.True(t, status.AcceleratorExporterDaemonSetsReady)
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
		metricsRemoteWriteURL: "http://example.com/write",
		acceleratorMgr:        accelerator.NewManager(gin.New()),
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

func TestSupportsClusterVersionAtLeast(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    bool
		wantErr bool
	}{
		{name: "empty", version: "", want: false},
		{name: "before minimum", version: "v1.0.0", want: false},
		{name: "minimum", version: "v1.1.0", want: true},
		{name: "nightly minimum", version: "v1.1.0-nightly-20260603", want: true},
		{name: "invalid", version: "invalid", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := supportsClusterVersionAtLeast(tt.version, MinKubeStateMetricsClusterVersion)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
