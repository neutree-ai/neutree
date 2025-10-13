package metrics

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.openly.dev/pointy"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

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
