package metrics

import (
	"context"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// GetMetricsResources returns all Kubernetes resources for the metrics component
func (m *MetricsComponent) GetMetricsResources() ([]client.Object, error) {
	var resources []client.Object

	// Build service account
	serviceAccount, err := m.buildVMAgentServiceAccount()
	if err != nil {
		return nil, errors.Wrap(err, "failed to build vmagent service account")
	}
	resources = append(resources, serviceAccount)

	// Build role
	role, err := m.buildVMAgentRole()
	if err != nil {
		return nil, errors.Wrap(err, "failed to build vmagent role")
	}
	resources = append(resources, role)

	// Build role binding
	roleBinding, err := m.buildVMAgentRoleBinding()
	if err != nil {
		return nil, errors.Wrap(err, "failed to build vmagent role binding")
	}
	resources = append(resources, roleBinding)

	// Build vmagent config ConfigMap
	configMap, err := m.buildVMAgentConfigMap()
	if err != nil {
		return nil, errors.Wrap(err, "failed to build vmagent config map")
	}
	resources = append(resources, configMap)

	// Build vmagent scrape config ConfigMap (kept for compatibility, but not used with kubernetes_sd_configs)
	scrapeConfigMap, err := m.buildVMAgentScrapeConfigMap()
	if err != nil {
		return nil, errors.Wrap(err, "failed to build vmagent scrape config map")
	}
	resources = append(resources, scrapeConfigMap)

	// Build vmagent deployment
	deployment, err := m.buildVMAgentDeployment()
	if err != nil {
		return nil, errors.Wrap(err, "failed to build vmagent deployment")
	}
	resources = append(resources, deployment)

	return resources, nil
}

// ApplyResources applies all metrics resources to the cluster
func (m *MetricsComponent) ApplyResources(ctx context.Context) error {
	resources, err := m.GetMetricsResources()
	if err != nil {
		return errors.Wrap(err, "failed to get metrics resources")
	}

	for _, resource := range resources {
		// Create or update the resource
		err := m.ctrlClient.Patch(ctx, resource, client.Apply,
			client.ForceOwnership,
			client.FieldOwner("neutree"))
		if err != nil {
			return errors.Wrapf(err, "failed to apply resource %s/%s",
				resource.GetObjectKind().GroupVersionKind().Kind,
				resource.GetName())
		}

		klog.InfoS("Applied metrics resource",
			"kind", resource.GetObjectKind().GroupVersionKind().Kind,
			"name", resource.GetName(),
			"namespace", resource.GetNamespace())
	}

	return nil
}

// DeleteResources deletes all metrics resources from the cluster
func (m *MetricsComponent) DeleteResources(ctx context.Context) (bool, error) {
	resources, err := m.GetMetricsResources()
	if err != nil {
		return false, errors.Wrap(err, "failed to get metrics resources")
	}

	deleted := true

	for _, resource := range resources {
		err = m.ctrlClient.Get(ctx, client.ObjectKey{
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

		err := m.ctrlClient.Delete(ctx, resource)
		if err != nil && !errors.Is(err, client.IgnoreNotFound(err)) {
			return false, errors.Wrapf(err, "failed to delete resource %s/%s",
				resource.GetObjectKind().GroupVersionKind().Kind,
				resource.GetName())
		}
	}

	return deleted, nil
}
