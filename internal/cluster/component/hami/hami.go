package hami

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
)

type HAMiComponent struct {
	cluster         *v1.Cluster
	namespace       string
	imagePrefix     string
	imagePullSecret string
	config          v1.KubernetesClusterConfig
	ctrlClient      client.Client
	pluginProvider  plugin.AcceleratorPluginProvider
	logger          klog.Logger
}

func NewHAMiComponent(cluster *v1.Cluster, namespace, imagePrefix, imagePullSecret string,
	config v1.KubernetesClusterConfig, ctrlClient client.Client,
	providers ...plugin.AcceleratorPluginProvider) *HAMiComponent {
	var pluginProvider plugin.AcceleratorPluginProvider
	if len(providers) > 0 {
		pluginProvider = providers[0]
	}

	return &HAMiComponent{
		cluster:         cluster,
		namespace:       namespace,
		imagePrefix:     imagePrefix,
		imagePullSecret: imagePullSecret,
		config:          config,
		ctrlClient:      ctrlClient,
		pluginProvider:  pluginProvider,
		logger: klog.LoggerWithValues(klog.Background(),
			"cluster", cluster.Metadata.WorkspaceName(),
			"component", ComponentName,
		),
	}
}

func (h *HAMiComponent) Reconcile() error {
	if err := h.Preflight(context.Background()); err != nil {
		h.setNotReadyStatus("PreflightFailed", err.Error())
		return err
	}

	scopePlan, err := h.ReconcileNodeScope(context.Background())
	if err != nil {
		h.setNotReadyStatus("NodeScopeFailed", err.Error())
		return errors.Wrap(err, "failed to reconcile HAMi node scope")
	}

	schedulerExisted, err := h.schedulerDeploymentExists(context.Background())
	if err != nil {
		h.setNotReadyStatus("SchedulerLookupFailed", err.Error())
		return errors.Wrap(err, "failed to get HAMi scheduler deployment")
	}

	tlsChanged, err := h.EnsureTLS(context.Background())
	if err != nil {
		h.setNotReadyStatus("TLSFailed", err.Error())
		return errors.Wrap(err, "failed to ensure HAMi webhook TLS")
	}

	if err := h.ApplyResources(context.Background(), scopePlan); err != nil {
		h.setNotReadyStatus("ApplyFailed", err.Error())
		return errors.Wrap(err, "failed to apply HAMi resources")
	}

	if _, err := h.PatchWebhookCABundle(context.Background()); err != nil {
		h.setNotReadyStatus("WebhookCABundleFailed", err.Error())
		return errors.Wrap(err, "failed to patch HAMi webhook caBundle")
	}

	if tlsChanged && schedulerExisted {
		if err := h.rolloutScheduler(context.Background()); err != nil {
			h.setNotReadyStatus("SchedulerRolloutFailed", err.Error())
			return errors.Wrap(err, "failed to rollout HAMi scheduler")
		}
	}

	status, err := h.CheckResourcesStatus(context.Background())
	if err != nil {
		h.setNotReadyStatus("StatusCheckFailed", err.Error())
		return errors.Wrap(err, "failed to check HAMi status")
	}

	if !status.Ready {
		h.setNotReadyStatus(status.Reason, status.Message)
		return hamiStatusError(status)
	}

	h.writeStatus(status.ComponentStatus())

	return nil
}

func (h *HAMiComponent) Delete() error {
	ownsNodeScope := h.ownsNodeScope()

	// Remove the cluster-wide scheduling scope before deleting HAMi Pods. A
	// force delete can remove the Neutree cluster record while Kubernetes
	// resources are still terminating, so delaying scope cleanup until every
	// manifest is gone can leave stale vGPU labels behind.
	if ownsNodeScope {
		if err := h.DisableNodeScope(context.Background()); err != nil {
			return errors.Wrap(err, "failed to disable HAMi node scope")
		}
	}

	deleted, err := h.DeleteResources(context.Background())
	if err != nil {
		return errors.Wrap(err, "failed to delete HAMi resources")
	}

	if !deleted {
		return errors.New("HAMi resources are not fully deleted, please wait")
	}

	if err := h.CleanupTLS(context.Background()); err != nil {
		return errors.Wrap(err, "failed to clean up HAMi TLS secret")
	}

	h.clearStatus()

	return nil
}

func (h *HAMiComponent) UpdateStatus(ctx context.Context) error {
	status, err := h.CheckResourcesStatus(ctx)
	if err != nil {
		h.setNotReadyStatus("StatusCheckFailed", err.Error())
		return nil
	}

	h.writeStatus(status.ComponentStatus())

	return nil
}

func (h *HAMiComponent) setNotReadyStatus(reason, message string) {
	h.writeStatus(&v1.ComponentStatus{
		Phase:   v1.ComponentPhaseNotReady,
		Managed: true,
		Version: Version,
		Reason:  reason,
		Message: message,
	})
}

func hamiStatusError(status *HAMiStatus) error {
	if status == nil {
		return fmt.Errorf("accelerator virtualization component is not ready")
	}

	return fmt.Errorf("accelerator virtualization component is not ready: %s %s",
		status.Reason, status.Message)
}

func (h *HAMiComponent) writeStatus(status *v1.ComponentStatus) {
	if h.cluster.Status == nil {
		h.cluster.Status = &v1.ClusterStatus{}
	}

	if h.cluster.Status.ComponentStatus == nil {
		h.cluster.Status.ComponentStatus = map[string]*v1.ComponentStatus{}
	}

	h.cluster.Status.ComponentStatus[v1.ComponentStatusAcceleratorVirtualizationKey] = status
}

func (h *HAMiComponent) hasStatus() bool {
	if h.cluster.Status == nil || h.cluster.Status.ComponentStatus == nil {
		return false
	}

	_, ok := h.cluster.Status.ComponentStatus[v1.ComponentStatusAcceleratorVirtualizationKey]

	return ok
}

func (h *HAMiComponent) ownsNodeScope() bool {
	return h.hasStatus() || (h.cluster.Spec != nil && h.cluster.Spec.AcceleratorVirtualizationEnabled())
}

func (h *HAMiComponent) clearStatus() {
	if h.cluster.Status == nil || h.cluster.Status.ComponentStatus == nil {
		return
	}

	delete(h.cluster.Status.ComponentStatus, v1.ComponentStatusAcceleratorVirtualizationKey)
}
