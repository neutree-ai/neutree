package hami

import (
	"context"
	"fmt"
	"time"

	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/neutree-ai/neutree/internal/util"
)

func (h *HAMiComponent) CheckResourcesStatus(ctx context.Context) (*HAMiStatus, error) {
	status := &HAMiStatus{}

	nodeList := &corev1.NodeList{}
	if err := h.ctrlClient.List(ctx, nodeList); err != nil {
		return nil, errors.Wrap(err, "failed to list nodes")
	}

	plan, err := h.planNodeScope(ctx, nodeList.Items, true)
	if err != nil {
		status.Reason = "AcceleratorPluginNotReady"
		status.Message = err.Error()

		return status, nil
	}

	status.EnabledNodes = append([]string{}, plan.EnabledNodes...)
	status.DisabledNodes = append([]string{}, plan.DisabledNodes...)
	status.StaleEnabledNodes = append([]string{}, plan.StaleEnabledNodes...)
	status.PatchedNodes = append([]string{}, plan.PatchedNodes...)
	status.ReadyNodes = len(plan.EnabledNodes)
	status.DesiredNodes = len(plan.EnabledNodes) + len(plan.PatchedNodes)
	status.VirtualizationMode = plan.Mode
	status.SupportedResources = plan.SupportedResources

	schedulerReady, schedulerReadyReplicas, schedulerReplicas, err := h.deploymentReady(ctx, SchedulerName)
	status.SchedulerReady = schedulerReady
	status.SchedulerReadyReplicas = schedulerReadyReplicas
	status.SchedulerReplicas = schedulerReplicas

	if err != nil {
		status.Reason = "SchedulerNotReady"
		status.Message = err.Error()

		return status, nil
	}

	deviceConfigReady, err := h.deviceConfigReady(ctx)
	status.DeviceConfigReady = deviceConfigReady

	if err != nil {
		status.Reason = "DeviceConfigNotReady"
		status.Message = err.Error()

		return status, nil
	}

	tlsReady, err := h.tlsReady(ctx)
	status.TLSReady = tlsReady

	if err != nil {
		status.Reason = "TLSNotReady"
		status.Message = err.Error()

		return status, nil
	}

	webhookReady, err := h.webhookReady(ctx)
	status.WebhookReady = webhookReady

	if err != nil {
		status.Reason = "WebhookNotReady"
		status.Message = err.Error()

		return status, nil
	}

	status.Ready = status.SchedulerReady && status.DeviceConfigReady && status.TLSReady && status.WebhookReady
	if status.Ready {
		status.Reason = "Ready"
		status.Message = "accelerator virtualization component is ready"
	}

	return status, nil
}

func (h *HAMiComponent) deviceConfigReady(ctx context.Context) (bool, error) {
	configMap := &corev1.ConfigMap{}
	if err := h.ctrlClient.Get(ctx, types.NamespacedName{
		Name:      SchedulerName + "-device",
		Namespace: h.namespace,
	}, configMap); err != nil {
		return false, errors.Wrap(err, "failed to get scheduler device config")
	}

	return true, nil
}

func (h *HAMiComponent) deploymentReady(ctx context.Context, name string) (bool, int, int, error) {
	deployment := &appsv1.Deployment{}
	if err := h.ctrlClient.Get(ctx, types.NamespacedName{Name: name, Namespace: h.namespace}, deployment); err != nil {
		return false, 0, 0, errors.Wrapf(err, "failed to get deployment %s", name)
	}

	ready := util.IsDeploymentUpdatedAndReady(deployment)

	return ready, int(deployment.Status.ReadyReplicas), int(deployment.Status.Replicas), nil
}

func (h *HAMiComponent) tlsReady(ctx context.Context) (bool, error) {
	secret := &corev1.Secret{}
	if err := h.ctrlClient.Get(ctx, types.NamespacedName{Name: TLSSecretName, Namespace: h.namespace}, secret); err != nil {
		return false, errors.Wrap(err, "failed to get TLS secret")
	}

	if servingCertificateNeedsRenewal(secret, time.Now()) {
		return false, errors.New("TLS secret is missing, expired, or inside renewal window")
	}

	return true, nil
}

func (h *HAMiComponent) webhookReady(ctx context.Context) (bool, error) {
	webhook := &unstructured.Unstructured{}
	webhook.SetAPIVersion("admissionregistration.k8s.io/v1")
	webhook.SetKind("MutatingWebhookConfiguration")

	if err := h.ctrlClient.Get(ctx, types.NamespacedName{Name: WebhookName}, webhook); err != nil {
		return false, errors.Wrap(err, "failed to get webhook")
	}

	webhooks, found, err := unstructured.NestedSlice(webhook.Object, "webhooks")
	if err != nil {
		return false, err
	}

	if !found || len(webhooks) == 0 {
		return false, errors.New("webhook list is empty")
	}

	for i := range webhooks {
		webhookItem, ok := webhooks[i].(map[string]interface{})
		if !ok {
			return false, fmt.Errorf("webhook %d is malformed", i)
		}

		caBundle, found, _ := unstructured.NestedString(webhookItem, "clientConfig", "caBundle")
		if !found || caBundle == "" {
			return false, fmt.Errorf("webhook %d has empty caBundle", i)
		}
	}

	return true, nil
}
