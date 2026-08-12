package controllers

import (
	"strconv"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/model_registry"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// syncWorkspaceModelRegistry provisions this deployment's built-in model
// registries into a workspace, idempotently, on every reconcile — the same shape
// as the built-in engines beside it.
//
// The switch governs provisioning only. Turning it off stops new registries being
// created; it does not remove ones already provisioned, which stay until a person
// deletes them.
func (c *WorkspaceController) syncWorkspaceModelRegistry(workspace v1.Workspace) error {
	workspaceName := workspace.Metadata.Name

	for _, desired := range model_registry.BuiltinModelRegistries(c.builtinRegistries, workspaceName) {
		existing, err := c.listModelRegistriesByName(workspaceName, desired.Metadata.Name)
		if err != nil {
			return err
		}

		if err := c.createOrUpdateBuiltinModelRegistry(desired, existing); err != nil {
			return errors.Wrapf(err, "failed to create or update built-in model registry %s for workspace %s",
				desired.Metadata.Name, workspaceName)
		}
	}

	return nil
}

func (c *WorkspaceController) createOrUpdateBuiltinModelRegistry(desired *v1.ModelRegistry,
	existing []v1.ModelRegistry) error {
	if len(existing) == 0 {
		return c.storage.CreateModelRegistry(desired)
	}

	current := existing[0]

	// Not ours. Adopting it would start managing a resource its owner is using;
	// leaving it alone only means this deployment has no built-in registry here.
	if current.Metadata == nil || !v1.IsBuiltin(current.Metadata.Annotations) {
		klog.V(4).Infof("model registry %s/%s exists and is not built-in, leaving it alone",
			desired.Metadata.Workspace, desired.Metadata.Name)

		return nil
	}

	// Being reaped. Re-pointing it would race the deletion; once the row is gone the
	// next reconcile provisions it afresh.
	if current.Metadata.DeletionTimestamp != "" {
		return nil
	}

	if current.Spec != nil && current.Spec.Type == desired.Spec.Type && current.Spec.Url == desired.Spec.Url {
		return nil
	}

	klog.Infof("Updating built-in model registry %s/%s to %s",
		desired.Metadata.Workspace, desired.Metadata.Name, desired.Spec.Url)

	// Credentials are carried over: a token a user attached is theirs, and PostgREST
	// replaces the whole composite, so omitting it would delete it.
	spec := *desired.Spec
	if current.Spec != nil {
		spec.Credentials = current.Spec.Credentials
	}

	return c.storage.UpdateModelRegistry(strconv.Itoa(current.ID), &v1.ModelRegistry{Spec: &spec})
}

func (c *WorkspaceController) listModelRegistriesByName(workspace, name string) ([]v1.ModelRegistry, error) {
	registries, err := c.storage.ListModelRegistry(storage.ListOption{
		Filters: []storage.Filter{
			{
				Column:   "metadata->workspace",
				Operator: "eq",
				Value:    strconv.Quote(workspace),
			},
			{
				Column:   "metadata->name",
				Operator: "eq",
				Value:    strconv.Quote(name),
			},
		},
	})
	if err != nil {
		return nil, errors.Wrapf(err, "failed to list model registries for workspace %s", workspace)
	}

	return registries, nil
}

// DeleteWorkspaceModelRegistry removes the built-in registries the control plane
// put in a workspace that is being torn down.
//
// Only built-in ones: a registry the user created is theirs, and workspace
// deletion is already refused while any of those remain. Without this they would
// be left behind as rows pointing at a workspace that no longer exists, since
// nothing else claims them — the deletion validator deliberately does not count
// them.
func (c *WorkspaceController) DeleteWorkspaceModelRegistry(workspace *v1.Workspace) error {
	registries, err := c.storage.ListModelRegistry(storage.ListOption{
		Filters: []storage.Filter{
			{
				Column:   "metadata->workspace",
				Operator: "eq",
				Value:    strconv.Quote(workspace.Metadata.Name),
			},
		},
	})
	if err != nil {
		return errors.Wrapf(err, "failed to list model registries for workspace %s", workspace.Metadata.Name)
	}

	for i := range registries {
		registry := registries[i]
		if registry.Metadata == nil || !v1.IsBuiltin(registry.Metadata.Annotations) {
			continue
		}

		if err := c.storage.DeleteModelRegistry(strconv.Itoa(registry.ID)); err != nil {
			return errors.Wrapf(err, "failed to delete built-in model registry %s/%s",
				workspace.Metadata.Name, registry.Metadata.Name)
		}
	}

	return nil
}
