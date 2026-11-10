package operator

import (
	"context"
	"encoding/json"
	"maps"
	"slices"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/neutree-ai/neutree/internal/accelerator/operator/manifests"
	"github.com/neutree-ai/neutree/internal/accelerator/resourceparser"
)

const operatorNodeCleanupKey = "node-agent-cleanup"

type operatorNodeCleanupState struct {
	DaemonSetUIDs []types.UID `json:"daemonSetUIDs"`
	Nodes         []string    `json:"nodes"`
}

// Keep the writer's scope across deletion retries, including after its controller disappears.
func captureOperatorNodeCleanup(ctx context.Context, opCtx *OperatorContext, inventoryName string) (*operatorNodeCleanupState, bool, error) {
	config := &corev1.ConfigMap{}
	err := opCtx.Client.Get(ctx, client.ObjectKey{Namespace: opCtx.Namespace, Name: inventoryName}, config)

	missing := apierrors.IsNotFound(err)
	if err != nil && !missing {
		return nil, false, err
	}

	if missing {
		config = &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: inventoryName, Namespace: opCtx.Namespace, Labels: operatorScopeLabels(opCtx)}}
	}

	state := &operatorNodeCleanupState{}

	previous := config.Data[operatorNodeCleanupKey]
	if previous != "" {
		if err := json.Unmarshal([]byte(previous), state); err != nil {
			return nil, false, err
		}
	}

	daemonSet := &appsv1.DaemonSet{}
	if err := opCtx.Client.Get(ctx, client.ObjectKey{Namespace: opCtx.Namespace, Name: manifests.NodeAgentName}, daemonSet); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, false, err
		}
	} else if daemonSet.UID != "" && scopedOperatorObject(opCtx, daemonSet) && !slices.Contains(state.DaemonSetUIDs, daemonSet.UID) {
		state.DaemonSetUIDs = append(state.DaemonSetUIDs, daemonSet.UID)
	}

	if len(state.DaemonSetUIDs) == 0 {
		return state, false, nil
	}

	pods := &corev1.PodList{}
	if err := opCtx.Client.List(ctx, pods, client.InNamespace(opCtx.Namespace)); err != nil {
		return nil, false, err
	}

	writersPending := false

	for _, pod := range pods.Items {
		owner := metav1.GetControllerOf(&pod)
		if owner == nil || owner.Kind != daemonSetKind || owner.Name != manifests.NodeAgentName || !slices.Contains(state.DaemonSetUIDs, owner.UID) {
			continue
		}

		writersPending = true

		if pod.Spec.NodeName != "" && !slices.Contains(state.Nodes, pod.Spec.NodeName) {
			state.Nodes = append(state.Nodes, pod.Spec.NodeName)
		}
	}

	slices.Sort(state.Nodes)

	data, err := json.Marshal(state)
	if err != nil {
		return nil, false, err
	}

	if string(data) != previous {
		base := config.DeepCopy()

		config.Data = maps.Clone(config.Data)
		if config.Data == nil {
			config.Data = map[string]string{}
		}

		config.Data[operatorNodeCleanupKey] = string(data)
		if missing {
			err = opCtx.Client.Create(ctx, config)
		} else {
			err = opCtx.Client.Patch(ctx, config, client.MergeFrom(base))
		}

		if err != nil {
			return nil, false, err
		}
	}

	return state, writersPending, nil
}

// OS providers have no client-label claim, so they use the persisted NodeAgent bindings.
func cleanupOperatorNodeAnnotations(ctx context.Context, opCtx *OperatorContext, boundNodes []string) error {
	provider := providerForContext(opCtx)
	nodes := &corev1.NodeList{}

	if err := opCtx.Client.List(ctx, nodes); err != nil {
		return err
	}

	for i := range nodes.Items {
		node := &nodes.Items[i]
		owner := node.Annotations[ClientDeployOwnerAnnotation]

		if owner != "" && owner != provider.Owner {
			continue
		}

		if owner != provider.Owner && !slices.Contains(boundNodes, node.Name) {
			continue
		}

		if _, exists := node.Annotations[resourceparser.NeutreeAcceleratorDevicesAnnotation]; exists {
			base := node.DeepCopy()
			delete(node.Annotations, resourceparser.NeutreeAcceleratorDevicesAnnotation)

			if err := opCtx.Client.Patch(ctx, node, client.MergeFrom(base)); err != nil {
				return err
			}
		}

		if owner == provider.Owner {
			if err := provider.releaseClientLabelManagement(ctx, node); err != nil {
				return err
			}
		}
	}

	return nil
}
