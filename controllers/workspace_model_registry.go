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
// registries into a workspace, idempotently, on every reconcile of a created
// workspace — the same shape as the built-in engines next to it. A registry
// someone deleted by hand comes back, and a change to the configured mirror
// address is picked up without a restart.
//
// The switch governs provisioning only. Turning it off stops the control plane
// putting registries there; it does not take away registries already provisioned,
// which stay until a person removes them. That asymmetry is deliberate: enabling
// the option is a statement about what this deployment offers from now on, while
// removing a registry that endpoints may be serving models from is a decision
// with consequences someone should be present for.
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

	// Someone else's registry that happens to share the name. Provisioning over it
	// would take a resource its owner is using and start managing it; leaving it
	// alone costs only that this deployment has no built-in registry here, which
	// is visible and fixable by renaming either one.
	if current.Metadata == nil || !v1.IsBuiltin(current.Metadata.Annotations) {
		klog.V(4).Infof("model registry %s/%s exists and is not built-in, leaving it alone",
			desired.Metadata.Workspace, desired.Metadata.Name)

		return nil
	}

	// On its way out because a person removed it. Re-pointing a row that is being
	// reaped would race the deletion; once the row is gone the next reconcile
	// provisions it afresh, which is what "the control plane keeps this here"
	// means.
	if current.Metadata.DeletionTimestamp != "" {
		return nil
	}

	if current.Spec != nil && current.Spec.Type == desired.Spec.Type && current.Spec.Url == desired.Spec.Url {
		return nil
	}

	klog.Infof("Updating built-in model registry %s/%s to %s",
		desired.Metadata.Workspace, desired.Metadata.Name, desired.Spec.Url)

	// Only the spec column is sent, and the credentials in it are carried over: a
	// token a user attached to the built-in registry is theirs, and PostgREST
	// replaces the whole composite, so leaving it out would delete it.
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
