package hami

import (
	"context"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
	clustervalidation "github.com/neutree-ai/neutree/internal/cluster/validation"
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
		h.cluster.Spec.AcceleratorVirtualization.ConfigPatch); err != nil {
		return err
	}

	if err := h.validateUnmanagedHAMi(ctx); err != nil {
		return err
	}

	if err := h.validateNoExistingVirtualization(ctx); err != nil {
		return err
	}

	return nil
}

func (h *HAMiComponent) validateUnmanagedHAMi(ctx context.Context) error {
	// Avoid adopting a pre-existing HAMi installation. Neutree relies on labels
	// and lifecycle ownership to render, update, and delete the component safely.
	checks, err := h.unmanagedHAMiResourceChecks()
	if err != nil {
		return err
	}

	for _, check := range checks {
		if err := h.validateManagedObject(ctx, check.object, check.key); err != nil {
			return err
		}
	}

	return nil
}

type managedObjectCheck struct {
	object client.Object
	key    types.NamespacedName
}

func (h *HAMiComponent) unmanagedHAMiResourceChecks() ([]managedObjectCheck, error) {
	rendered, err := h.renderResources(NodeScopePlan{
		NodeScopeLabel: defaultNodeScopeLabel(),
	})
	if err != nil {
		return nil, errors.Wrap(err, "failed to render HAMi manifests for unmanaged resource validation")
	}

	checks := make([]managedObjectCheck, 0, len(rendered.Items))

	for i := range rendered.Items {
		item := &rendered.Items[i]
		if item.GetAPIVersion() == "" || item.GetKind() == "" || item.GetName() == "" {
			continue
		}

		object := &unstructured.Unstructured{}
		object.SetAPIVersion(item.GetAPIVersion())
		object.SetKind(item.GetKind())

		checks = append(checks, managedObjectCheck{
			object: object,
			key: types.NamespacedName{
				Namespace: item.GetNamespace(),
				Name:      item.GetName(),
			},
		})
	}

	return checks, nil
}

func (h *HAMiComponent) validateManagedObject(ctx context.Context, obj client.Object, key types.NamespacedName) error {
	kind := objectKind(obj)

	if err := h.ctrlClient.Get(ctx, key, obj); err != nil {
		return clientIgnoreNotFound(err)
	}

	if obj.GetLabels()[ManagedComponentLabelKey] != ManagedComponentLabelValue {
		if kind == "MutatingWebhookConfiguration" && key.Name == WebhookName {
			return errors.New("found existing unmanaged HAMi webhook hami-webhook")
		}

		return errors.Errorf("found existing unmanaged HAMi resource %s/%s", kind, key.Name)
	}

	return nil
}

func objectKind(obj client.Object) string {
	if kind := obj.GetObjectKind().GroupVersionKind().Kind; kind != "" {
		return kind
	}

	return "Object"
}

// validateNoExistingVirtualization checks whether another Neutree cluster
// already manages accelerator virtualization on the underlying Kubernetes
// cluster. The check only runs on the initial deploy; restarts (where the
// cluster already has a HAMi component status) are allowed through.
func (h *HAMiComponent) validateNoExistingVirtualization(ctx context.Context) error {
	if h.hasStatus() {
		return nil
	}

	nodeList := &corev1.NodeList{}
	if err := h.ctrlClient.List(ctx, nodeList); err != nil {
		return errors.Wrap(err, "failed to list nodes for virtualization conflict check")
	}

	for _, node := range nodeList.Items {
		if node.Labels[plugin.NvidiaGPUVirtualizationLabelKey] == "true" {
			return errors.New(
				"another Neutree cluster already manages accelerator virtualization on this Kubernetes cluster")
		}
	}

	return nil
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
