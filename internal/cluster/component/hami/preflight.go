package hami

import (
	"context"

	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
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

	for _, check := range h.unmanagedHAMiResourceChecks() {
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

func (h *HAMiComponent) unmanagedHAMiResourceChecks() []managedObjectCheck {
	return []managedObjectCheck{
		{object: &corev1.ServiceAccount{}, key: types.NamespacedName{Name: SchedulerName, Namespace: h.namespace}},
		{object: &rbacv1.Role{}, key: types.NamespacedName{Name: SchedulerName, Namespace: h.namespace}},
		{object: &rbacv1.RoleBinding{}, key: types.NamespacedName{Name: SchedulerName, Namespace: h.namespace}},
		{object: &rbacv1.ClusterRole{}, key: types.NamespacedName{Name: SchedulerName}},
		{object: &rbacv1.ClusterRoleBinding{}, key: types.NamespacedName{Name: SchedulerName}},
		{object: &rbacv1.ClusterRoleBinding{}, key: types.NamespacedName{Name: SchedulerName + "-kube"}},
		{object: &rbacv1.ClusterRoleBinding{}, key: types.NamespacedName{Name: SchedulerName + "-volume"}},
		{object: &corev1.ConfigMap{}, key: types.NamespacedName{Name: SchedulerName, Namespace: h.namespace}},
		{object: &corev1.ConfigMap{}, key: types.NamespacedName{Name: SchedulerName + "-device", Namespace: h.namespace}},
		{object: &corev1.Service{}, key: types.NamespacedName{Name: SchedulerName, Namespace: h.namespace}},
		{object: &appsv1.Deployment{}, key: types.NamespacedName{Name: SchedulerName, Namespace: h.namespace}},
		{object: &corev1.ServiceAccount{}, key: types.NamespacedName{Name: DevicePluginDaemonSetName, Namespace: h.namespace}},
		{object: &rbacv1.ClusterRole{}, key: types.NamespacedName{Name: DevicePluginDaemonSetName + "-monitor"}},
		{object: &rbacv1.ClusterRoleBinding{}, key: types.NamespacedName{Name: DevicePluginDaemonSetName}},
		{object: &corev1.ConfigMap{}, key: types.NamespacedName{Name: DevicePluginDaemonSetName, Namespace: h.namespace}},
		{object: &corev1.Service{}, key: types.NamespacedName{Name: MonitorServiceName, Namespace: h.namespace}},
		{object: &appsv1.DaemonSet{}, key: types.NamespacedName{Name: DevicePluginDaemonSetName, Namespace: h.namespace}},
	}
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
	case *corev1.ServiceAccount:
		return "ServiceAccount"
	case *corev1.ConfigMap:
		return "ConfigMap"
	case *rbacv1.Role:
		return "Role"
	case *rbacv1.RoleBinding:
		return "RoleBinding"
	case *rbacv1.ClusterRole:
		return "ClusterRole"
	case *rbacv1.ClusterRoleBinding:
		return "ClusterRoleBinding"
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
