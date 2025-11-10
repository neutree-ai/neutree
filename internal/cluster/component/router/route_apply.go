package router

import (
	"context"
	"encoding/json"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/manifest_apply"
	"github.com/neutree-ai/neutree/internal/util"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// GetRouteResources returns all Kubernetes resources for the route component
func (r *RouterComponent) GetRouteResources() (*unstructured.UnstructuredList, error) {
	variables := r.buildManifestVariables()

	objs, err := util.RenderKubernetesManifest(routerMainifestTemplate, variables)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to render router manifest for cluster %s", r.cluster.Metadata.Name)
	}

	return objs, nil
}

// ApplyResources applies all route resources to the cluster
func (r *RouterComponent) ApplyResources(ctx context.Context) error {
	objs, err := r.GetRouteResources()
	if err != nil {
		return errors.Wrapf(err, "failed to get route resources for cluster %s", r.cluster.Metadata.Name)
	}

	lastAppliedConfigJson := r.cluster.Metadata.Annotations[routerLastAppliedConfigAnnotation]
	manifestApplier := manifest_apply.NewManifestApply(r.ctrlClient, r.namespace).WithNewObjects(objs).
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
		WithLogger(r.logger)

	changedCount, err := manifestApplier.ApplyManifests(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to apply manifests")
	}

	if changedCount > 0 {
		r.logger.Info("Applied router manifests",
			"changedObjects", changedCount)

		// Save the current configuration as last applied config
		// Only marshal the Items array, not the entire UnstructuredList
		newConfigJSON, err := json.Marshal(objs.Items)
		if err != nil {
			return errors.Wrap(err, "failed to marshal deployment objects")
		}

		// Initialize annotations if needed
		if r.cluster.Metadata.Annotations == nil {
			r.cluster.Metadata.Annotations = make(map[string]string)
		}

		// Update last applied config in annotations
		r.cluster.Metadata.Annotations[routerLastAppliedConfigAnnotation] = string(newConfigJSON)

		r.logger.Info("Updated router configuration")
	}

	return nil
}

// DeleteResources deletes all route resources from the cluster
func (r *RouterComponent) DeleteResources(ctx context.Context) (bool, error) {
	lastAppliedConfigJson := r.cluster.Metadata.Annotations[routerLastAppliedConfigAnnotation]
	manifestApplier := manifest_apply.NewManifestApply(r.ctrlClient, r.namespace).
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
		WithLogger(r.logger)

	deleted, err := manifestApplier.Delete(ctx)
	if err != nil {
		return false, errors.Wrap(err, "failed to delete manifests")
	}

	if deleted {
		r.logger.Info("Deleted all router resources")
	}

	return deleted, nil
}
