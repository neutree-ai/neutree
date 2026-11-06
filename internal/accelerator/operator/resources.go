package operator

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/neutree-ai/neutree/internal/deploy"
)

// ApplyResources applies renderer output with the repository's Kubernetes
// deployer so the operator gets the same last-applied tracking and delete
// semantics as other cluster components.
func ApplyResources(ctx context.Context, opCtx *OperatorContext, resources *unstructured.UnstructuredList) error {
	if opCtx == nil || opCtx.Client == nil || opCtx.Cluster == nil || opCtx.Cluster.Metadata == nil {
		return fmt.Errorf("operator context and Kubernetes client are required")
	}

	if resources == nil {
		return nil
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
		"accelerator-operator-"+opCtx.Cluster.Metadata.Name,
	).WithNewObjects(resources).WithLabels(labels).Apply(ctx)
	if err != nil {
		return fmt.Errorf("apply accelerator operator resources: %w", err)
	}

	return nil
}
