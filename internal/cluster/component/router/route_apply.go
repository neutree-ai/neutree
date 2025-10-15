package router

import (
	"context"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// GetRouteResources returns all Kubernetes resources for the route component
func (r *RouterComponent) GetRouteResources() ([]client.Object, error) {
	var resources []client.Object

	// Build service account
	serviceAccount, err := r.buildRouterServiceAccount()
	if err != nil {
		return nil, errors.Wrap(err, "failed to build router service account")
	}
	resources = append(resources, serviceAccount)

	// Build role
	role, err := r.buildRouterRole()
	if err != nil {
		return nil, errors.Wrap(err, "failed to build router role")
	}
	resources = append(resources, role)

	// Build role binding
	roleBinding, err := r.buildRouterRoleBinding()
	if err != nil {
		return nil, errors.Wrap(err, "failed to build router role binding")
	}
	resources = append(resources, roleBinding)

	// Build service
	service, err := r.buildRouteService()
	if err != nil {
		return nil, errors.Wrap(err, "failed to build route service")
	}
	resources = append(resources, service)

	// Build deployment
	deployment, err := r.buildRouterDeployment()
	if err != nil {
		return nil, errors.Wrap(err, "failed to build router deployment")
	}
	resources = append(resources, deployment)

	return resources, nil
}

// ApplyResources applies all route resources to the cluster
func (r *RouterComponent) ApplyResources(ctx context.Context) error {
	resources, err := r.GetRouteResources()
	if err != nil {
		return errors.Wrap(err, "failed to get route resources")
	}

	for _, resource := range resources {
		// Set owner reference if needed
		// Add labels/annotations as needed

		// Create or update the resource
		err := r.ctrlClient.Patch(ctx, resource, client.Apply,
			client.ForceOwnership,
			client.FieldOwner("neutree"))
		if err != nil {
			return errors.Wrapf(err, "failed to apply resource %s/%s",
				resource.GetObjectKind().GroupVersionKind().Kind,
				resource.GetName())
		}

		klog.InfoS("Applied route resource",
			"kind", resource.GetObjectKind().GroupVersionKind().Kind,
			"name", resource.GetName(),
			"namespace", resource.GetNamespace())
	}

	return nil
}

// DeleteResources deletes all route resources from the cluster
func (r *RouterComponent) DeleteResources(ctx context.Context) (bool, error) {
	resources, err := r.GetRouteResources()
	if err != nil {
		return false, errors.Wrap(err, "failed to get route resources")
	}

	deleted := true

	for _, resource := range resources {
		err = r.ctrlClient.Get(ctx, client.ObjectKey{
			Name:      resource.GetName(),
			Namespace: resource.GetNamespace(),
		}, resource)
		if err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return deleted, errors.Wrapf(err, "failed to get resource %s/%s",
				resource.GetObjectKind().GroupVersionKind().Kind,
				resource.GetName())
		}

		deleted = false

		if resource.GetDeletionTimestamp() != nil {
			// Already marked for deletion
			continue
		}

		err := r.ctrlClient.Delete(ctx, resource)
		if err != nil && !errors.Is(err, client.IgnoreNotFound(err)) {
			return false, errors.Wrapf(err, "failed to delete resource %s/%s",
				resource.GetObjectKind().GroupVersionKind().Kind,
				resource.GetName())
		}
	}

	return deleted, nil
}
