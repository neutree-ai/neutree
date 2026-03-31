package deploy

import (
	"context"
	"encoding/json"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// KubernetesDeployer orchestrates the complete deployment workflow for Kubernetes resources
// It handles: configuration loading -> manifest application -> configuration saving
type KubernetesDeployer struct {
	ctrlClient    client.Client
	namespace     string
	resourceName  string // resource name (cluster name, endpoint name, etc.)
	componentName string // component name (router, metrics, deployment, etc.)

	configStore *ConfigStore
	newObjects  *unstructured.UnstructuredList
	mutates     []Mutate
	labels      map[string]string
	logger      klog.Logger
}

// NewKubernetesDeployer creates a new KubernetesDeployer instance
// resourceName: the name of the resource being deployed (e.g., cluster name, endpoint name)
// componentName: the name of the component (e.g., router, metrics, deployment)
func NewKubernetesDeployer(ctrlClient client.Client, namespace, resourceName, componentName string) *KubernetesDeployer {
	return &KubernetesDeployer{
		ctrlClient:    ctrlClient,
		namespace:     namespace,
		resourceName:  resourceName,
		componentName: componentName,
		configStore:   NewConfigStore(ctrlClient),
		logger:        klog.Background(),
	}
}

// WithNewObjects sets the desired state objects to apply
func (a *KubernetesDeployer) WithNewObjects(objects *unstructured.UnstructuredList) *KubernetesDeployer {
	a.newObjects = objects
	return a
}

// WithMutate sets the mutation callback for modifying objects before applying
// This is optional and can be used for advanced customization beyond label setting
func (a *KubernetesDeployer) WithMutate(mutate Mutate) *KubernetesDeployer {
	a.mutates = append(a.mutates, mutate)
	return a
}

// WithLabels sets labels to be applied to all Kubernetes resources
// The labels will be automatically applied to all objects before deployment
// This also sets the labels for the ConfigMap metadata
func (a *KubernetesDeployer) WithLabels(labels map[string]string) *KubernetesDeployer {
	a.labels = labels

	// Create a mutate function that applies these labels to all objects
	a.mutates = append(a.mutates, func(obj *unstructured.Unstructured) error {
		objLabels := obj.GetLabels()
		if objLabels == nil {
			objLabels = make(map[string]string)
		}

		// Merge user labels into object labels
		for k, v := range labels {
			objLabels[k] = v
		}

		obj.SetLabels(objLabels)

		return nil
	})

	return a
}

// WithLogger sets the logger for deployment operations
func (a *KubernetesDeployer) WithLogger(logger klog.Logger) *KubernetesDeployer {
	a.logger = logger
	return a
}

// Apply applies the resources to Kubernetes with automatic configuration management
// Returns the number of changed objects and any error
func (a *KubernetesDeployer) Apply(ctx context.Context) (int, error) {
	if a.newObjects == nil {
		return 0, errors.New("newObjects is required")
	}

	// 1. Load last applied config from ConfigMap
	lastAppliedConfig, err := a.configStore.Get(ctx, a.namespace, a.resourceName, a.componentName)
	if err != nil {
		a.logger.Error(err, "Failed to load last applied config, using empty",
			"resourceName", a.resourceName,
			"componentName", a.componentName)

		lastAppliedConfig = ""
	}

	// 2. Apply manifests using ManifestApply
	manifestApply := NewManifestApply(a.ctrlClient, a.namespace).
		WithLastAppliedConfig(lastAppliedConfig).
		WithNewObjects(a.newObjects).
		WithLogger(a.logger)

	for _, mutate := range a.mutates {
		manifestApply = manifestApply.WithMutate(mutate)
	}

	changedCount, err := manifestApply.ApplyManifests(ctx)
	if err != nil {
		return 0, errors.Wrap(err, "failed to apply manifests")
	}

	// 3. Save new config to ConfigMap if there were changes
	if changedCount > 0 {
		newConfigJSON, err := json.Marshal(a.newObjects.Items)
		if err != nil {
			a.logger.Error(err, "Failed to marshal new config")
			// Don't return error as resources were successfully applied
		} else {
			err = a.configStore.Set(ctx, a.namespace, a.resourceName, a.componentName, string(newConfigJSON), a.labels)
			if err != nil {
				a.logger.Error(err, "Failed to save config to ConfigMap")
				// Don't return error as resources were successfully applied
			} else {
				a.logger.Info("Saved configuration",
					"resourceName", a.resourceName,
					"componentName", a.componentName)
			}
		}
	}

	return changedCount, nil
}

// Delete removes the resources from Kubernetes and cleans up configuration
// Returns true if deletion is complete, false if still in progress
func (a *KubernetesDeployer) Delete(ctx context.Context) (bool, error) {
	// 1. Load last applied config from ConfigMap
	lastAppliedConfig, err := a.configStore.Get(ctx, a.namespace, a.resourceName, a.componentName)
	if err != nil {
		a.logger.Error(err, "Failed to load last applied config for deletion")
		return false, err
	}

	if lastAppliedConfig == "" {
		a.logger.V(4).Info("No last applied config found, nothing to delete")
		return true, nil
	}

	// 2. Delete resources using ManifestApply
	manifestApply := NewManifestApply(a.ctrlClient, a.namespace).
		WithLastAppliedConfig(lastAppliedConfig).
		WithLogger(a.logger)

	deleteFinished, err := manifestApply.Delete(ctx)
	if err != nil {
		return deleteFinished, err
	}

	// 3. Clean up ConfigMap if deletion is complete
	if deleteFinished {
		err = a.configStore.Delete(ctx, a.namespace, a.resourceName, a.componentName)
		if err != nil {
			a.logger.Error(err, "Failed to delete ConfigMap")
			// Don't return error as resources were successfully deleted
		} else {
			a.logger.Info("Deleted configuration",
				"resourceName", a.resourceName,
				"componentName", a.componentName)
		}
	}

	return deleteFinished, nil
}
