package orchestrator

import (
	"context"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/neutree-ai/neutree/internal/util"
)

func cleanupUnexpectedAdmissionPods(ctx *OrchestratorContext) {
	callCtx := context.Background()
	namespace := util.ClusterNamespace(ctx.Cluster)
	deployment := &appsv1.Deployment{}

	if err := ctx.ctrClient.Get(callCtx, client.ObjectKey{Namespace: namespace, Name: ctx.Endpoint.Metadata.Name}, deployment); err != nil {
		if client.IgnoreNotFound(err) != nil {
			ctx.logger.Error(err, "Failed to find deployment for admission Pod cleanup")
		}

		return
	}

	selector, err := metav1.LabelSelectorAsSelector(deployment.Spec.Selector)
	if err != nil {
		ctx.logger.Error(err, "Failed to read deployment selector for admission Pod cleanup")
		return
	}

	pods := &corev1.PodList{}
	if err := ctx.ctrClient.List(callCtx, pods, client.InNamespace(namespace), client.MatchingLabelsSelector{Selector: selector}); err != nil {
		ctx.logger.Error(err, "Failed to list Pods for admission Pod cleanup")
		return
	}

	for index := range pods.Items {
		pod := &pods.Items[index]
		if pod.Status.Phase != corev1.PodFailed || pod.Status.Reason != "UnexpectedAdmissionError" {
			continue
		}

		// The Pod name may have been reused since the list; delete only the observed UID.
		if err := ctx.ctrClient.Delete(callCtx, pod, &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &pod.UID}}); err != nil {
			if client.IgnoreNotFound(err) != nil {
				ctx.logger.Error(err, "Failed to delete admission-failed Pod", "pod", pod.Name)
			}
		}
	}
}
