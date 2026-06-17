package hami

import (
	"context"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/deploy"
)

func (h *HAMiComponent) ApplyResources(ctx context.Context, scopePlan NodeScopePlan) error {
	objs, err := h.renderResources(scopePlan)
	if err != nil {
		return errors.Wrap(err, "failed to render HAMi manifests")
	}

	applier := deploy.NewKubernetesDeployer(
		h.ctrlClient,
		h.namespace,
		h.cluster.Metadata.Name,
		ComponentName,
	).WithNewObjects(objs).
		WithLabels(map[string]string{
			v1.NeutreeClusterLabelKey:          h.cluster.Metadata.Name,
			v1.NeutreeClusterWorkspaceLabelKey: h.cluster.Metadata.Workspace,
			v1.LabelManagedBy:                  v1.LabelManagedByValue,
			v1.NeutreeServingVersionLabel:      h.cluster.Spec.Version,
			ManagedComponentLabelKey:           ManagedComponentLabelValue,
		}).
		WithLogger(h.logger)

	changedCount, err := applier.Apply(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to apply HAMi manifests")
	}

	if changedCount > 0 {
		h.logger.Info("Applied HAMi manifests", "changedObjects", changedCount)
	}

	return nil
}

func (h *HAMiComponent) DeleteResources(ctx context.Context) (bool, error) {
	applier := deploy.NewKubernetesDeployer(
		h.ctrlClient,
		h.namespace,
		h.cluster.Metadata.Name,
		ComponentName,
	).WithLogger(h.logger)

	deleted, err := applier.Delete(ctx)
	if err != nil {
		return false, errors.Wrap(err, "failed to delete HAMi manifests")
	}

	return deleted, nil
}
