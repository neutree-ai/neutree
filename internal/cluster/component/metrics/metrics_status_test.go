package metrics

import (
	"context"
	"testing"
	"time"

	v1 "github.com/neutree-ai/neutree/api/v1"
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

func TestCheckResourcesStatusIncludesMetricsDiagnostics(t *testing.T) {
	now := metav1.NewTime(time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC))
	fakeClient := fake.NewClientBuilder().
		WithObjects(
			&appsv1.Deployment{
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
					Replicas:           1,
					UpdatedReplicas:    1,
					ReadyReplicas:      0,
					AvailableReplicas:  0,
					Conditions: []appsv1.DeploymentCondition{
						{
							Type:    appsv1.DeploymentAvailable,
							Status:  corev1.ConditionFalse,
							Reason:  "MinimumReplicasUnavailable",
							Message: "Deployment does not have minimum availability",
						},
					},
				},
			},
			&corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "vmagent-6f8d9c7b8c-abcde",
					Namespace: "default",
					Labels: map[string]string{
						"app":       "vmagent",
						"cluster":   "test",
						"workspace": "default",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: "vmagent",
							Env: []corev1.EnvVar{
								{Name: "SHOULD_NOT_LEAK", Value: "TOP_SECRET_VALUE"},
							},
						},
					},
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name: "vmagent",
							State: corev1.ContainerState{
								Waiting: &corev1.ContainerStateWaiting{
									Reason:  "CrashLoopBackOff",
									Message: "back-off restarting failed container",
								},
							},
						},
					},
				},
			},
			&corev1.Event{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "vmagent-backoff",
					Namespace: "default",
				},
				InvolvedObject: corev1.ObjectReference{
					Kind:      "Pod",
					Namespace: "default",
					Name:      "vmagent-6f8d9c7b8c-abcde",
				},
				Type:          corev1.EventTypeWarning,
				Reason:        "BackOff",
				Message:       "Back-off restarting failed container",
				LastTimestamp: now,
			},
		).
		Build()
	metricsCmpt := &MetricsComponent{
		ctrlClient: fakeClient,
		namespace:  "default",
		cluster: &v1.Cluster{
			Metadata: &v1.Metadata{Name: "test", Workspace: "default"},
			Spec:     &v1.ClusterSpec{Version: "v1.0.0"},
		},
	}

	status, err := metricsCmpt.CheckResourcesStatus(context.Background())

	assert.NoError(t, err)
	assert.False(t, status.DeploymentReady)
	message := status.String()
	assert.Contains(t, message, "deployment/vmagent")
	assert.Contains(t, message, "available=0")
	assert.Contains(t, message, "pod/vmagent-6f8d9c7b8c-abcde")
	assert.Contains(t, message, "CrashLoopBackOff")
	assert.Contains(t, message, "event/vmagent-6f8d9c7b8c-abcde")
	assert.Contains(t, message, "BackOff")
	assert.NotContains(t, message, "TOP_SECRET_VALUE")
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
		).
		Build()

	metricsCmpt := &MetricsComponent{
		ctrlClient: fakeClient,
		namespace:  "default",
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
	assert.Equal(t, 1, status.PodsReady)
	assert.Equal(t, 1, status.KubeStateMetricsPodsReady)
}

func TestCheckResourcesStatusSkipsKubeStateMetricsBeforeV110(t *testing.T) {
	fakeClient := fake.NewClientBuilder().
		WithObjects(readyMetricsDeployment("vmagent")).
		Build()

	metricsCmpt := &MetricsComponent{
		ctrlClient: fakeClient,
		namespace:  "default",
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
}

func TestSupportsKubeStateMetricsClusterVersion(t *testing.T) {
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
			got, err := supportsKubeStateMetricsClusterVersion(tt.version)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
