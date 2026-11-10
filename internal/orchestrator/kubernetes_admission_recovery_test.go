package orchestrator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCleanupUnexpectedAdmissionPodsOnlyDeletesCurrentEndpointPods(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "neutree",
			Name:      "endpoint",
			UID:       types.UID("deployment-uid"),
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{
				"app":      "inference",
				"endpoint": "endpoint",
			}},
		},
	}
	replicaSet := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       "neutree",
			Name:            "endpoint-rs",
			UID:             types.UID("replicaset-uid"),
			OwnerReferences: []metav1.OwnerReference{controllerOwnerRef(appsv1.SchemeGroupVersion.String(), deploymentKind, deployment.Name, deployment.UID)},
		},
	}

	tests := []struct {
		name       string
		pod        *corev1.Pod
		wantDelete bool
	}{
		{
			name:       "current endpoint admission rejection",
			pod:        endpointPod("rejected", replicaSet.Name, replicaSet.UID, corev1.PodFailed, unexpectedAdmissionErrorReason),
			wantDelete: true,
		},
		{
			name:       "different failure reason",
			pod:        endpointPod("evicted", replicaSet.Name, replicaSet.UID, corev1.PodFailed, "Evicted"),
			wantDelete: false,
		},
		{
			name:       "running endpoint",
			pod:        endpointPod("running", replicaSet.Name, replicaSet.UID, corev1.PodRunning, ""),
			wantDelete: false,
		},
		{
			name:       "same labels without endpoint owner",
			pod:        endpointPod("unowned", "", "", corev1.PodFailed, unexpectedAdmissionErrorReason),
			wantDelete: false,
		},
	}

	objects := []client.Object{deployment, replicaSet}
	for _, test := range tests {
		objects = append(objects, test.pod)
	}
	ctrlClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()

	remaining := (&kubernetesOrchestrator{}).cleanupUnexpectedAdmissionPods(
		context.Background(), ctrlClient, deployment, deployment.Name,
		[]corev1.Pod{*tests[0].pod, *tests[1].pod, *tests[2].pod, *tests[3].pod},
	)

	require.Len(t, remaining, 3)
	for _, test := range tests {
		pod := &corev1.Pod{}
		err := ctrlClient.Get(context.Background(), client.ObjectKey{Namespace: test.pod.Namespace, Name: test.pod.Name}, pod)
		if test.wantDelete {
			require.Error(t, err, test.name)
			continue
		}

		require.NoError(t, err, test.name)
	}
}

func TestPodOwnedByEndpointDeploymentRequiresReplicaSetOwnerChain(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))

	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Namespace: "neutree",
		Name:      "endpoint",
		UID:       types.UID("deployment-uid"),
	}}
	foreignReplicaSet := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
		Namespace: "neutree",
		Name:      "foreign-rs",
		UID:       types.UID("foreign-rs-uid"),
		OwnerReferences: []metav1.OwnerReference{
			controllerOwnerRef(appsv1.SchemeGroupVersion.String(), deploymentKind, "different-endpoint", types.UID("other-deployment-uid")),
		},
	}}
	pod := endpointPod("foreign-owner", foreignReplicaSet.Name, foreignReplicaSet.UID, corev1.PodFailed, unexpectedAdmissionErrorReason)
	ctrlClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(foreignReplicaSet).Build()

	require.False(t, podOwnedByEndpointDeployment(context.Background(), ctrlClient, pod, deployment, deployment.Name))
}

func endpointPod(name, ownerName string, ownerUID types.UID, phase corev1.PodPhase, reason string) *corev1.Pod {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Namespace: "neutree",
		Name:      name,
		Labels: map[string]string{
			"app":      "inference",
			"endpoint": "endpoint",
		},
	}, Status: corev1.PodStatus{Phase: phase, Reason: reason}}
	if ownerName != "" {
		pod.OwnerReferences = []metav1.OwnerReference{
			controllerOwnerRef(appsv1.SchemeGroupVersion.String(), "ReplicaSet", ownerName, ownerUID),
		}
	}

	return pod
}

func controllerOwnerRef(apiVersion, kind, name string, uid types.UID) metav1.OwnerReference {
	controller := true
	blockDeletion := true

	return metav1.OwnerReference{
		APIVersion:         apiVersion,
		Kind:               kind,
		Name:               name,
		UID:                uid,
		Controller:         &controller,
		BlockOwnerDeletion: &blockDeletion,
	}
}
