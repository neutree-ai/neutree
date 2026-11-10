package hami

import (
	"context"

	"github.com/pkg/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	acceleratoroperator "github.com/neutree-ai/neutree/internal/accelerator/operator"
	clustervalidation "github.com/neutree-ai/neutree/internal/cluster/validation"
	sharedhami "github.com/neutree-ai/neutree/internal/component/hami"
)

func (h *HAMiComponent) Preflight(ctx context.Context) error {
	if h.cluster.Spec == nil || !h.cluster.Spec.AcceleratorVirtualizationEnabled() {
		return errors.New("accelerator virtualization is not enabled")
	}

	if err := clustervalidation.ValidateAcceleratorVirtualizationClusterSupport(
		h.cluster.Spec.Type, h.cluster.GetVersion()); err != nil {
		return err
	}

	if err := clustervalidation.ValidateAcceleratorVirtualizationConfigPatch(
		acceleratoroperator.UserVirtualizationConfigPatch(h.cluster.Spec.AcceleratorVirtualization.ConfigPatch)); err != nil {
		return err
	}

	return sharedhami.ValidateWebhookOwnership(ctx, h.ctrlClient, h.cluster)
}

func clientIgnoreNotFound(err error) error {
	if err == nil {
		return nil
	}

	if apierrors.IsNotFound(err) {
		return nil
	}

	return err
}
