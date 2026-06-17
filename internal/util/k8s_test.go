package util

import (
	"testing"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestIsDeploymentUpdatedAndReady(t *testing.T) {
	replicas := int32(1)

	tests := []struct {
		name       string
		status     appsv1.DeploymentStatus
		wantReady  bool
		assertNote string
	}{
		{
			name: "ready deployment",
			status: appsv1.DeploymentStatus{
				ObservedGeneration: 1,
				Replicas:           1,
				UpdatedReplicas:    1,
				ReadyReplicas:      1,
				AvailableReplicas:  1,
				Conditions: []appsv1.DeploymentCondition{
					{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionTrue},
					{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue},
				},
			},
			wantReady: true,
		},
		{
			name: "rollout stuck with old replica still serving",
			status: appsv1.DeploymentStatus{
				ObservedGeneration: 1,
				Replicas:           2,
				UpdatedReplicas:    1,
				ReadyReplicas:      1,
				AvailableReplicas:  1,
				Conditions: []appsv1.DeploymentCondition{
					{
						Type:    appsv1.DeploymentProgressing,
						Status:  corev1.ConditionTrue,
						Reason:  "ReplicaSetUpdated",
						Message: "ReplicaSet is progressing.",
					},
					{
						Type:   appsv1.DeploymentAvailable,
						Status: corev1.ConditionTrue,
						Reason: "MinimumReplicasAvailable",
					},
				},
			},
			wantReady:  false,
			assertNote: "old replicas must be gone before a rollout is considered complete",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Generation: 1,
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: &replicas,
				},
				Status: tt.status,
			}

			assert.Equal(t, tt.wantReady, IsDeploymentUpdatedAndReady(deployment), tt.assertNote)
		})
	}
}
