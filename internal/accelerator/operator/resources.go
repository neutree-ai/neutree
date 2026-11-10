package operator

import (
	"context"
	"encoding/json"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/deploy"
)

// ApplyResources applies renderer output with the repository's Kubernetes
// deployer so the operator gets the same last-applied tracking and delete
// semantics as other cluster components.
func ApplyResources(ctx context.Context, opCtx *OperatorContext, resources *unstructured.UnstructuredList) error {
	if opCtx == nil || opCtx.Cluster == nil || opCtx.Cluster.Metadata == nil {
		return fmt.Errorf("operator context and Kubernetes client are required")
	}

	return applyResources(ctx, opCtx, "accelerator-operator-"+opCtx.Cluster.GetName(), resources)
}

// ApplyScopeResources applies one operator-owned scope with an independent
// deployer inventory. Independent inventories prevent one scope from pruning
// resources owned by another scope.
func ApplyScopeResources(ctx context.Context, opCtx *OperatorContext, scope string, resources *unstructured.UnstructuredList) error {
	return applyResources(ctx, opCtx, nvidiaScopeComponent(scope), resources)
}

func applyResources(ctx context.Context, opCtx *OperatorContext, componentName string, resources *unstructured.UnstructuredList) error {
	if opCtx == nil || opCtx.Client == nil || opCtx.Cluster == nil || opCtx.Cluster.Metadata == nil {
		return fmt.Errorf("operator context and Kubernetes client are required")
	}

	if resources == nil {
		return nil
	}

	if err := waitForLegacyResources(ctx, opCtx, resources); err != nil {
		return err
	}

	labels := map[string]string{
		"neutree.ai/accelerator-operator":        "nvidia",
		"neutree.ai/accelerator-deployment-mode": "operator",
	}
	if opCtx.Cluster.Metadata.Name != "" {
		labels["neutree.ai/cluster"] = opCtx.Cluster.Metadata.Name
		labels["neutree.ai/workspace"] = opCtx.Cluster.Metadata.Workspace
	}

	_, err := deploy.NewKubernetesDeployer(
		opCtx.Client,
		opCtx.Namespace,
		opCtx.Cluster.Metadata.Name,
		componentName,
	).WithNewObjects(resources).WithLabels(labels).WithMutate(func(object *unstructured.Unstructured) error {
		OperatorResourceLabels(object, opCtx.Cluster)

		return nil
	}).Apply(ctx)
	if err != nil {
		return fmt.Errorf("apply accelerator operator resources: %w", err)
	}

	// Status must not use an older inventory after the generic deployer's best-effort save fails.
	expected, err := json.Marshal(resources.Items)
	if err != nil {
		return fmt.Errorf("encode accelerator operator inventory: %w", err)
	}

	saved, err := deploy.NewConfigStore(opCtx.Client).Get(ctx, opCtx.Namespace, opCtx.Cluster.GetName(), componentName)
	if err != nil {
		return fmt.Errorf("read accelerator operator inventory: %w", err)
	}

	if saved != string(expected) {
		return fmt.Errorf("accelerator operator inventory was not saved for the current resources")
	}

	return nil
}

// waitForLegacyResources lets Hami prune its data-plane objects before the
// Operator adopts the same resource names. Foreign owners remain protected.
func waitForLegacyResources(ctx context.Context, opCtx *OperatorContext, desired *unstructured.UnstructuredList) error {
	for index := range desired.Items {
		object := &desired.Items[index]
		live := &unstructured.Unstructured{}
		live.SetGroupVersionKind(object.GroupVersionKind())

		if err := opCtx.Client.Get(ctx, client.ObjectKeyFromObject(object), live); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return err
		}

		labels := live.GetLabels()
		if labels[OperatorOwnerLabel] == OperatorOwnerValue &&
			labels[v1.NeutreeClusterLabelKey] == opCtx.Cluster.GetName() &&
			labels[v1.NeutreeClusterWorkspaceLabelKey] == opCtx.Cluster.GetWorkspace() {
			continue
		}

		if labels[OperatorOwnerLabel] != "" &&
			(labels[OperatorOwnerLabel] != OperatorOwnerValue ||
				labels[v1.NeutreeClusterLabelKey] != opCtx.Cluster.GetName() ||
				labels[v1.NeutreeClusterWorkspaceLabelKey] != opCtx.Cluster.GetWorkspace()) {
			return fmt.Errorf("operator resource %s/%s is owned by another accelerator deployment", object.GetKind(), object.GetName())
		}

		return fmt.Errorf("operator resource %s/%s is waiting for Legacy cleanup", object.GetKind(), object.GetName())
	}

	return nil
}
