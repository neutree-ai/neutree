package hami

import (
	"context"
	"fmt"
	"strings"

	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator"
)

func (h *HAMiComponent) Preflight(ctx context.Context) error {
	if h.cluster.Spec == nil || !h.cluster.Spec.AcceleratorVirtualizationEnabled() {
		return errors.New("accelerator virtualization is not enabled")
	}

	if h.cluster.Spec.Type != v1.KubernetesClusterType {
		return errors.New("accelerator virtualization component is only supported for Kubernetes clusters")
	}

	if err := h.validateClusterVersion(); err != nil {
		return err
	}

	if err := validateConfigPatch(h.cluster.Spec.AcceleratorVirtualization.ConfigPatch); err != nil {
		return err
	}

	if err := h.validateUnmanagedHAMi(ctx); err != nil {
		return err
	}

	return nil
}

func (h *HAMiComponent) validateClusterVersion() error {
	version := h.cluster.GetVersion()

	supported, err := accelerator.SupportsVirtualizationClusterVersion(version)
	if err != nil {
		return fmt.Errorf("failed to parse cluster version %q: %w", version, err)
	}

	if !supported {
		return fmt.Errorf("accelerator virtualization component requires cluster version >= %s",
			accelerator.MinVirtualizationClusterVersion)
	}

	return nil
}

func validateConfigPatch(configPatch map[string]interface{}) error {
	if configPatch == nil {
		return nil
	}

	for key := range configPatch {
		switch key {
		case "devicePlugin", "scheduler", "global", "dra":
		default:
			return fmt.Errorf("unsupported accelerator_virtualization.config_patch key %q", key)
		}
	}

	if dra, ok := nestedBool(configPatch, "dra", "enabled"); ok && dra {
		return errors.New("HAMi DRA is not supported")
	}

	if schedulerPatch, ok := nestedBool(configPatch, "scheduler", "patch", "enabled"); ok && schedulerPatch {
		return errors.New("HAMi scheduler patch hook is managed by Neutree and cannot be enabled")
	}

	if certManager, ok := nestedBool(configPatch, "scheduler", "certManager", "enabled"); ok && certManager {
		return errors.New("HAMi cert-manager integration is managed by Neutree and cannot be enabled")
	}

	// Neutree vGPU support is based on HAMi core mode. MIG mode requires
	// different node/device semantics and is intentionally rejected here.
	if migStrategy, ok := nestedString(configPatch, "devicePlugin", "migStrategy"); ok &&
		strings.ToLower(strings.TrimSpace(migStrategy)) != "none" {
		return errors.New("HAMi MIG virtualization mode is not supported")
	}

	return nil
}

func nestedBool(values map[string]interface{}, path ...string) (bool, bool) {
	var current interface{} = values
	for _, key := range path {
		asMap, ok := current.(map[string]interface{})
		if !ok {
			return false, false
		}

		current, ok = asMap[key]
		if !ok {
			return false, false
		}
	}

	value, ok := current.(bool)

	return value, ok
}

func nestedString(values map[string]interface{}, path ...string) (string, bool) {
	var current interface{} = values
	for _, key := range path {
		asMap, ok := current.(map[string]interface{})
		if !ok {
			return "", false
		}

		current, ok = asMap[key]
		if !ok {
			return "", false
		}
	}

	value, ok := current.(string)

	return value, ok
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
