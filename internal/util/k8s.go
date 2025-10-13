package util

import (
	"context"

	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const ResourceSkipPatchAnnotation = "neutree.io/skip-patch"

func CreateOrPatch(ctx context.Context, obj client.Object, ctrClient client.Client) error {
	if obj.GetAnnotations() != nil && obj.GetAnnotations()[ResourceSkipPatchAnnotation] != "" {
		return nil
	}

	err := ctrClient.Patch(ctx, obj, client.Apply, client.ForceOwnership, client.FieldOwner("neutree-controller"))
	if err != nil {
		return errors.Wrap(err, "failed to apply object")
	}

	return nil
}

func IsDeploymentUpdatedAndReady(deployment *appsv1.Deployment) bool {
	if deployment.Status.ObservedGeneration < deployment.Generation {
		return false
	}

	if deployment.Status.UpdatedReplicas != *deployment.Spec.Replicas {
		return false
	}

	if deployment.Status.ReadyReplicas != *deployment.Spec.Replicas {
		return false
	}

	if deployment.Status.AvailableReplicas != *deployment.Spec.Replicas {
		return false
	}

	progressing := false
	available := false

	for _, condition := range deployment.Status.Conditions {
		if condition.Type == appsv1.DeploymentProgressing && condition.Status == corev1.ConditionTrue {
			progressing = true
		}

		if condition.Type == appsv1.DeploymentAvailable && condition.Status == corev1.ConditionTrue {
			available = true
		}
	}

	if !progressing || !available {
		return false
	}

	return true
}
