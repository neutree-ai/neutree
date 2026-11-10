package operator

import (
	"context"
	"encoding/json"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/deploy"
)

// ReleaseOwnedResources removes operator-owned objects from a legacy component
// inventory before the legacy deployer computes its deletion diff.
func ReleaseOwnedResources(ctx context.Context, ctrlClient client.Client, namespace string, cluster *v1.Cluster,
	componentName string, operators map[string]AcceleratorOperator, excluded map[string]struct{}, labels map[string]string,
) error {
	if len(excluded) == 0 || ctrlClient == nil || cluster == nil || cluster.Metadata == nil {
		return nil
	}

	store := deploy.NewConfigStore(ctrlClient)
	config, err := store.Get(ctx, namespace, cluster.GetName(), componentName)

	if err != nil || config == "" {
		return err
	}

	var previous []unstructured.Unstructured
	if err := json.Unmarshal([]byte(config), &previous); err != nil {
		return err
	}

	retained := make([]unstructured.Unstructured, 0, len(previous))

	for _, object := range previous {
		live := &unstructured.Unstructured{}
		live.SetGroupVersionKind(object.GroupVersionKind())

		if err := ctrlClient.Get(ctx, client.ObjectKeyFromObject(&object), live); err != nil {
			if client.IgnoreNotFound(err) != nil {
				return err
			}
		}

		if !ResourceOwnedBy(cluster, namespace, operators, excluded, live) {
			retained = append(retained, object)
		}
	}

	if len(retained) == len(previous) {
		return nil
	}

	data, err := json.Marshal(retained)
	if err != nil {
		return err
	}

	return store.Set(ctx, namespace, cluster.GetName(), componentName, string(data), labels)
}

// ResourceOwnedBy checks the same cluster, namespace, accelerator and
// operator labels used by resource apply/delete paths.
func ResourceOwnedBy(cluster *v1.Cluster, namespace string, operators map[string]AcceleratorOperator,
	excluded map[string]struct{}, object *unstructured.Unstructured) bool {
	if cluster == nil || cluster.Metadata == nil || object == nil {
		return false
	}

	labels := object.GetLabels()
	if labels[DeploymentModeLabel] != DeploymentModeValue ||
		labels[v1.NeutreeClusterLabelKey] != cluster.GetName() ||
		labels[v1.NeutreeClusterWorkspaceLabelKey] != cluster.GetWorkspace() ||
		(object.GetNamespace() != namespace && object.GetNamespace() != "") {
		return false
	}

	for acceleratorType, acceleratorOperator := range operators {
		if _, selected := excluded[acceleratorType]; !selected || acceleratorOperator == nil {
			continue
		}

		if labels[OperatorOwnerLabel] == acceleratorOperator.Name() {
			return true
		}
	}

	return false
}
