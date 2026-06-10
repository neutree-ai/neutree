package hami

import (
	"context"
	"fmt"
	"time"

	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

	schedulerReady, schedulerReadyReplicas, schedulerReplicas, err := h.deploymentReady(ctx, SchedulerName)
	status.SchedulerReady = schedulerReady
	status.SchedulerReadyReplicas = schedulerReadyReplicas
	status.SchedulerReplicas = schedulerReplicas

	if err != nil {
		status.Reason = "SchedulerNotReady"
		status.Message = err.Error()

		return status, nil
	}

	devicePluginReady, devicePluginReadyPods, devicePluginPods, err := h.daemonSetReady(ctx, DevicePluginDaemonSetName)
	status.DevicePluginReady = devicePluginReady
	status.DevicePluginReadyPods = devicePluginReadyPods
	status.DevicePluginPods = devicePluginPods

	if err != nil {
		status.Reason = "DaemonSetNotReady"
		status.Message = err.Error()

		return status, nil
	}

	monitorReady, err := h.monitorReady(ctx, plan, devicePluginReady)
	status.MonitorReady = monitorReady
	status.MonitorReadyPods = devicePluginReadyPods
	status.MonitorPods = devicePluginPods

	if err != nil {
		status.Reason = "MonitorNotReady"
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

	status.Ready = status.SchedulerReady && status.DevicePluginReady && status.MonitorReady && status.TLSReady && status.WebhookReady
	if status.Ready {
		status.Reason = "Ready"
		status.Message = "accelerator virtualization component is ready"
	}

	return status, nil
}

func (h *HAMiComponent) deploymentReady(ctx context.Context, name string) (bool, int, int, error) {
	deployment := &appsv1.Deployment{}
	if err := h.ctrlClient.Get(ctx, types.NamespacedName{Name: name, Namespace: h.namespace}, deployment); err != nil {
		return false, 0, 0, errors.Wrapf(err, "failed to get deployment %s", name)
	}

	ready := util.IsDeploymentUpdatedAndReady(deployment)

	return ready, int(deployment.Status.ReadyReplicas), int(deployment.Status.Replicas), nil
}

func (h *HAMiComponent) daemonSetReady(ctx context.Context, name string) (bool, int, int, error) {
	ds := &appsv1.DaemonSet{}
	if err := h.ctrlClient.Get(ctx, types.NamespacedName{Name: name, Namespace: h.namespace}, ds); err != nil {
		if name == DevicePluginDaemonSetName && apierrors.IsNotFound(err) {
			nodeList := &corev1.NodeList{}
			if listErr := h.ctrlClient.List(ctx, nodeList); listErr == nil {
				plan, planErr := h.planNodeScope(ctx, nodeList.Items, true)
				if planErr == nil && (allCandidateNodesExplicitlyDisabled(plan) ||
					len(plan.EnabledNodes)+len(plan.PatchedNodes)+len(plan.DisabledNodes) == 0) {
					return true, 0, 0, nil
				}
			}
		}

		return false, 0, 0, errors.Wrapf(err, "failed to get daemonset %s", name)
	}

	desired := ds.Status.DesiredNumberScheduled
	readyScheduled := desired == ds.Status.NumberReady
	updatedScheduled := desired == ds.Status.UpdatedNumberScheduled
	availableScheduled := desired == ds.Status.NumberAvailable
	ready := readyScheduled && updatedScheduled && availableScheduled

	return ready, int(ds.Status.NumberReady), int(ds.Status.DesiredNumberScheduled), nil
}

func (h *HAMiComponent) monitorReady(ctx context.Context, plan NodeScopePlan, devicePluginReady bool) (bool, error) {
	if !shouldDeployDevicePlugin(plan) {
		return true, nil
	}

	if !devicePluginReady {
		return false, errors.New("HAMi device plugin daemonset is not ready")
	}

	service := &corev1.Service{}
	if err := h.ctrlClient.Get(ctx, types.NamespacedName{Name: MonitorServiceName, Namespace: h.namespace}, service); err != nil {
		return false, errors.Wrapf(err, "failed to get service %s", MonitorServiceName)
	}

	return true, nil
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
