package util

import (
	"context"

	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const ResourceSkipPatchAnnotation = "neutree.ai/skip-patch"

func CreateOrPatch(ctx context.Context, obj client.Object, ctrClient client.Client) error {
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

	if deployment.Spec.Replicas == nil {
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

func ToUnstructured(obj runtime.Object) (*unstructured.Unstructured, error) {
	if u, ok := obj.(*unstructured.Unstructured); ok {
		return u, nil
	}

	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, err
	}

	u := &unstructured.Unstructured{Object: m}
	u.SetGroupVersionKind(obj.GetObjectKind().GroupVersionKind())

	return u, nil
}
