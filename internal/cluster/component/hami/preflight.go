package hami

import (
	"context"

	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

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

	return nil
}

func (h *HAMiComponent) validateUnmanagedHAMi(ctx context.Context) error {
	// Avoid adopting a pre-existing HAMi installation. Neutree relies on labels
	// and lifecycle ownership to render, update, and delete the component safely.
	webhook := &unstructured.Unstructured{}
	webhook.SetAPIVersion("admissionregistration.k8s.io/v1")
	webhook.SetKind("MutatingWebhookConfiguration")

	err := h.ctrlClient.Get(ctx, types.NamespacedName{Name: WebhookName}, webhook)
	if err != nil {
		if err := clientIgnoreNotFound(err); err != nil {
			return err
		}
	} else if webhook.GetLabels()[ManagedComponentLabelKey] != ManagedComponentLabelValue {
		return errors.New("found existing unmanaged HAMi webhook hami-webhook")
	}

	objects := []client.Object{
		&appsv1.Deployment{},
		&appsv1.DaemonSet{},
		&appsv1.DaemonSet{},
		&corev1.Service{},
		&corev1.Service{},
		&corev1.Service{},
	}
	keys := []types.NamespacedName{
		{Name: SchedulerName, Namespace: h.namespace},
		{Name: DevicePluginDaemonSetName, Namespace: h.namespace},
		{Name: MonitorDaemonSetName, Namespace: h.namespace},
		{Name: SchedulerName, Namespace: h.namespace},
		{Name: MonitorServiceName, Namespace: h.namespace},
		{Name: MonitorDaemonSetName, Namespace: h.namespace},
	}

	for i, obj := range objects {
		if err := h.validateManagedObject(ctx, obj, keys[i]); err != nil {
			return err
		}
	}

	return nil
}

func (h *HAMiComponent) validateManagedObject(ctx context.Context, obj client.Object, key types.NamespacedName) error {
	if err := h.ctrlClient.Get(ctx, key, obj); err != nil {
		return clientIgnoreNotFound(err)
	}

	if obj.GetLabels()[ManagedComponentLabelKey] != ManagedComponentLabelValue {
		return errors.Errorf("found existing unmanaged HAMi resource %s/%s",
			objectKind(obj), key.Name)
	}

	return nil
}

func objectKind(obj client.Object) string {
	switch obj.(type) {
	case *appsv1.Deployment:
		return "Deployment"
	case *appsv1.DaemonSet:
		return "DaemonSet"
	case *corev1.Service:
		return "Service"
	default:
		if kind := obj.GetObjectKind().GroupVersionKind().Kind; kind != "" {
			return kind
		}

		return "Object"
	}
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
