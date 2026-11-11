package accelerator

import (
	"context"
	"fmt"

	nodev1 "k8s.io/api/node/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// SelectRuntimeClass only reads existing classes; Operator reconciliation owns creation.
func SelectRuntimeClass(ctx context.Context, reader client.Reader, cluster *v1.Cluster, policies []GPUOperatorClusterPolicy) (string, error) {
	for _, policy := range policies {
		name, _, err := unstructured.NestedString(policy.Spec, "operator", "runtimeClass")
		if err != nil {
			return "", fmt.Errorf("read ClusterPolicy %s operator.runtimeClass: %w", policy.Name, err)
		}

		if name == "" {
			continue
		}

		class := &nodev1.RuntimeClass{}
		if err := reader.Get(ctx, client.ObjectKey{Name: name}, class); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}

			return "", fmt.Errorf("get RuntimeClass %s: %w", name, err)
		}

		return name, nil
	}

	standard := &nodev1.RuntimeClass{}
	if err := reader.Get(ctx, client.ObjectKey{Name: NvidiaDefaultRuntimeClass}, standard); err != nil {
		if !apierrors.IsNotFound(err) {
			return "", fmt.Errorf("get RuntimeClass %s: %w", NvidiaDefaultRuntimeClass, err)
		}
	} else if standard.Handler == NvidiaDefaultRuntimeClass {
		return standard.Name, nil
	}

	fallback := &nodev1.RuntimeClass{}
	if err := reader.Get(ctx, client.ObjectKey{Name: NeutreeNvidiaRuntimeClass}, fallback); err != nil {
		return "", client.IgnoreNotFound(err)
	}

	if err := validateOwnedRuntimeClass(fallback, cluster); err != nil {
		return "", err
	}

	return fallback.Name, nil
}

func EnsureRuntimeClass(ctx context.Context, ctrlClient client.Client, cluster *v1.Cluster, policies []GPUOperatorClusterPolicy) error {
	selected, err := SelectRuntimeClass(ctx, ctrlClient, cluster, policies)
	if err != nil || selected != "" {
		return err
	}

	class := &nodev1.RuntimeClass{
		ObjectMeta: metav1.ObjectMeta{Name: NeutreeNvidiaRuntimeClass, Labels: map[string]string{
			v1.LabelManagedBy:         v1.LabelManagedByValue,
			v1.NeutreeClusterLabelKey: cluster.GetName(), v1.NeutreeClusterWorkspaceLabelKey: cluster.GetWorkspace(),
		}},
		Handler: NvidiaDefaultRuntimeClass,
	}
	if err := ctrlClient.Create(ctx, class); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create RuntimeClass %s: %w", class.Name, err)
		}

		existing := &nodev1.RuntimeClass{}
		if err := ctrlClient.Get(ctx, client.ObjectKey{Name: NeutreeNvidiaRuntimeClass}, existing); err != nil {
			return fmt.Errorf("get concurrently created RuntimeClass %s: %w", class.Name, err)
		}

		return validateOwnedRuntimeClass(existing, cluster)
	}

	return nil
}

// DeleteRuntimeClass uses creation ownership even if selection has moved to GPU Operator's class.
func DeleteRuntimeClass(ctx context.Context, ctrlClient client.Client, cluster *v1.Cluster) error {
	class := &nodev1.RuntimeClass{}
	if err := ctrlClient.Get(ctx, client.ObjectKey{Name: NeutreeNvidiaRuntimeClass}, class); err != nil {
		return client.IgnoreNotFound(err)
	}

	if !runtimeClassOwnedBy(class, cluster) {
		return nil
	}

	if err := ctrlClient.Delete(ctx, class, &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &class.UID}}); err != nil {
		return client.IgnoreNotFound(err)
	}

	return nil
}

func validateOwnedRuntimeClass(class *nodev1.RuntimeClass, cluster *v1.Cluster) error {
	if class.Handler != NvidiaDefaultRuntimeClass {
		return fmt.Errorf("RuntimeClass %s handler is %q, expected %q", class.Name, class.Handler, NvidiaDefaultRuntimeClass)
	}

	if !runtimeClassOwnedBy(class, cluster) {
		return fmt.Errorf("RuntimeClass %s is not managed by Neutree cluster %s/%s", class.Name, cluster.GetWorkspace(), cluster.GetName())
	}

	return nil
}

func runtimeClassOwnedBy(class *nodev1.RuntimeClass, cluster *v1.Cluster) bool {
	return class.Labels[v1.LabelManagedBy] == v1.LabelManagedByValue &&
		class.Labels[v1.NeutreeClusterLabelKey] == cluster.GetName() &&
		class.Labels[v1.NeutreeClusterWorkspaceLabelKey] == cluster.GetWorkspace()
}
