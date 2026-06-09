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

const legacyComponentStatusHAMiKey = "hami"

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
		h.setStatus(v1.ComponentPhaseNotReady, "PreflightFailed", err.Error())
		return err
	}

	scopePlan, err := h.ReconcileNodeScope(context.Background())
	if err != nil {
		h.setStatus(v1.ComponentPhaseNotReady, "NodeScopeFailed", err.Error())
		return errors.Wrap(err, "failed to reconcile HAMi node scope")
	}

	if err := h.EnsureTLS(context.Background()); err != nil {
		h.setStatus(v1.ComponentPhaseNotReady, "TLSFailed", err.Error())
		return errors.Wrap(err, "failed to ensure HAMi webhook TLS")
	}

	if err := h.ApplyResources(context.Background(), scopePlan); err != nil {
		h.setStatus(v1.ComponentPhaseNotReady, "ApplyFailed", err.Error())
		return errors.Wrap(err, "failed to apply HAMi resources")
	}

	if err := h.PatchWebhookCABundle(context.Background()); err != nil {
		h.setStatus(v1.ComponentPhaseNotReady, "WebhookCABundleFailed", err.Error())
		return errors.Wrap(err, "failed to patch HAMi webhook caBundle")
	}

	status, err := h.CheckResourcesStatus(context.Background())
	if err != nil {
		h.setStatus(v1.ComponentPhaseNotReady, "StatusCheckFailed", err.Error())
		return errors.Wrap(err, "failed to check HAMi status")
	}

	if !status.Ready {
		h.setStatus(v1.ComponentPhaseNotReady, status.Reason, status.Message)
		return fmt.Errorf("accelerator virtualization component is not fully ready: %s", status.Message)
	}

	h.writeStatus(status.ComponentStatus())
	return nil
}

func (h *HAMiComponent) Delete() error {
	deleted, err := h.DeleteResources(context.Background())
	if err != nil {
		return errors.Wrap(err, "failed to delete HAMi resources")
	}

	if !deleted {
		h.setStatus(v1.ComponentPhaseNotReady, "Deleting", "HAMi resources are still deleting")
		return errors.New("HAMi resources are not fully deleted, please wait")
	}

	if err := h.CleanupTLS(context.Background()); err != nil {
		h.setStatus(v1.ComponentPhaseNotReady, "TLSCleanupFailed", err.Error())
		return errors.Wrap(err, "failed to clean up HAMi TLS secret")
	}

	h.setStatus(v1.ComponentPhaseNotReady, "Disabled", "accelerator virtualization is disabled")
	return nil
}

func (h *HAMiComponent) UpdateStatus(ctx context.Context) error {
	status, err := h.CheckResourcesStatus(ctx)
	if err != nil {
		h.setStatus(v1.ComponentPhaseNotReady, "StatusCheckFailed", err.Error())
		return nil
	}

	h.writeStatus(status.ComponentStatus())
	return nil
}

func (h *HAMiComponent) setStatus(phase v1.ComponentPhase, reason, message string) {
	h.writeStatus(&v1.ComponentStatus{
		Phase:   phase,
		Managed: true,
		Version: Version,
		Reason:  reason,
		Message: message,
	})
}

func (h *HAMiComponent) writeStatus(status *v1.ComponentStatus) {
	if h.cluster.Status == nil {
		h.cluster.Status = &v1.ClusterStatus{}
	}
	if h.cluster.Status.ComponentStatus == nil {
		h.cluster.Status.ComponentStatus = map[string]*v1.ComponentStatus{}
	}
	h.cluster.Status.ComponentStatus[v1.ComponentStatusAcceleratorVirtualizationKey] = status
	delete(h.cluster.Status.ComponentStatus, legacyComponentStatusHAMiKey)
}
