package metrics

import (
	"context"
	"encoding/json"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/manifest_apply"
	"github.com/neutree-ai/neutree/internal/util"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// GetMetricsResources returns all Kubernetes resources for the metrics component
func (m *MetricsComponent) GetMetricsResources() (*unstructured.UnstructuredList, error) {
	variables := m.buildManifestVariables()

	objs, err := util.RenderKubernetesManifest(metricsManifestTemplate, variables)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to render metrics manifest for cluster %s", m.cluster.Metadata.Name)
	}

	return objs, nil
}

// ApplyResources applies all metrics resources to the cluster
func (m *MetricsComponent) ApplyResources(ctx context.Context) error {
	objs, err := m.GetMetricsResources()
	if err != nil {
		return errors.Wrapf(err, "failed to get metrics resources for cluster %s", m.cluster.Metadata.Name)
	}

	lastAppliedConfigJson := m.cluster.Metadata.Annotations[metricsLastAppliedConfigAnnotation]
	manifestApplier := manifest_apply.NewManifestApply(m.ctrlClient, m.namespace).
		WithNewObjects(objs).
		WithLastAppliedConfig(lastAppliedConfigJson).
		WithMutate(func(obj *unstructured.Unstructured) error {
			labels := obj.GetLabels()
			if labels == nil {
				labels = make(map[string]string)
			}
			labels[v1.LabelManagedBy] = v1.LabelManagedByValue
			obj.SetLabels(labels)
			return nil
		}).
		WithLogger(m.logger)

	changedCount, err := manifestApplier.ApplyManifests(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to apply manifests")
	}

	if changedCount > 0 {
		m.logger.Info("Applied metrics manifests",
			"changedObjects", changedCount)

		// Save the current configuration as last applied config
		// Only marshal the Items array, not the entire UnstructuredList
		newConfigJSON, err := json.Marshal(objs.Items)
		if err != nil {
			return errors.Wrap(err, "failed to marshal deployment objects")
		}

		// Initialize annotations if needed
		if m.cluster.Metadata.Annotations == nil {
			m.cluster.Metadata.Annotations = make(map[string]string)
		}

		// Update last applied config in annotations
		m.cluster.Metadata.Annotations[metricsLastAppliedConfigAnnotation] = string(newConfigJSON)

		m.logger.Info("Updated metrics configuration")
	}

	return nil
}

// DeleteResources deletes all metrics resources from the cluster
func (m *MetricsComponent) DeleteResources(ctx context.Context) (bool, error) {
	lastAppliedConfigJson := m.cluster.Metadata.Annotations[metricsLastAppliedConfigAnnotation]
	manifestApplier := manifest_apply.NewManifestApply(m.ctrlClient, m.namespace).
		WithLastAppliedConfig(lastAppliedConfigJson).
		WithMutate(func(obj *unstructured.Unstructured) error {
			labels := obj.GetLabels()
			if labels == nil {
				labels = make(map[string]string)
			}
			labels[v1.LabelManagedBy] = v1.LabelManagedByValue
			obj.SetLabels(labels)
			return nil
		}).
		WithLogger(m.logger)

	deleted, err := manifestApplier.Delete(ctx)
	if err != nil {
		return false, errors.Wrap(err, "failed to delete manifests")
	}

	if deleted {
		m.logger.Info("Deleted all metrics resources")
	}

	delete(m.cluster.Metadata.Annotations, metricsLastAppliedConfigAnnotation)

	return deleted, nil
}
