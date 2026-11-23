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

func TestGetPodLogContainersSpecOrder(test *testing.T) {
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers:     []corev1.Container{{Name: "engine"}, {Name: "sidecar"}},
			InitContainers: []corev1.Container{{Name: "download"}, {Name: "prepare"}},
		},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "sidecar", RestartCount: 4},
				{Name: "engine", RestartCount: 2},
			},
			InitContainerStatuses: []corev1.ContainerStatus{
				{Name: "prepare", RestartCount: 7},
				{Name: "download", RestartCount: 3},
			},
		},
	}
	assert.Equal(test, []ContainerLogInfo{
		{Name: "engine", RestartCount: 2, Started: true},
		{Name: "sidecar", RestartCount: 4, Started: true},
		{Name: "download", RestartCount: 3, Started: true},
		{Name: "prepare", RestartCount: 7, Started: true},
	}, GetPodLogContainers(pod))
}

func TestGetPodLogContainersCompletedInit(test *testing.T) {
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{InitContainers: []corev1.Container{{Name: "download"}}},
		Status: corev1.PodStatus{
			InitContainerStatuses: []corev1.ContainerStatus{{
				Name:  "download",
				State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}},
			}},
		},
	}

	assert.Equal(test, []ContainerLogInfo{
		{Name: "download", Started: true},
	}, GetPodLogContainers(pod))
}

func TestGetPodLogContainersStarted(test *testing.T) {
	startupProbeIncomplete := false
	cases := []struct {
		name     string
		statuses []corev1.ContainerStatus
		started  bool
	}{
		{
			name: "running before startup probe completes",
			statuses: []corev1.ContainerStatus{{
				Name: "inference", Started: &startupProbeIncomplete,
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
			}},
			started: true,
		},
		{
			name: "completed without restarting",
			statuses: []corev1.ContainerStatus{{
				Name: "inference", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}},
			}},
			started: true,
		},
		{
			name: "waiting after a restart",
			statuses: []corev1.ContainerStatus{{
				Name: "inference", RestartCount: 1,
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
			}},
			started: true,
		},
	}
	for _, testCase := range cases {
		for _, containerType := range []string{"container", "initContainer"} {
			test.Run(testCase.name+"/"+containerType, func(test *testing.T) {
				pod := &corev1.Pod{
					Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "inference"}}},
					Status: corev1.PodStatus{ContainerStatuses: testCase.statuses},
				}
				if containerType == "initContainer" {
					pod = &corev1.Pod{
						Spec:   corev1.PodSpec{InitContainers: []corev1.Container{{Name: "inference"}}},
						Status: corev1.PodStatus{InitContainerStatuses: testCase.statuses},
					}
				}
				assert.Equal(test, []ContainerLogInfo{{
					Name: "inference", RestartCount: testCase.statuses[0].RestartCount, Started: testCase.started,
				}}, GetPodLogContainers(pod))
			})
		}
	}
}
