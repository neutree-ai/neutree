package metrics

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator"
)

type MetricsComponent struct {
	cluster               *v1.Cluster
	namespace             string
	imagePrefix           string
	metricsRemoteWriteURL string
	imagePullSecret       string
	acceleratorMgr        accelerator.Manager

	config     v1.KubernetesClusterConfig
	ctrlClient client.Client
	logger     klog.Logger

	inferenceScrapeRules InferenceScrapeRules
	componentImages      ComponentImages
}

// ComponentImages contains Profile-resolved core Kubernetes metrics images.
// Accelerator-specific exporters remain controlled by accelerator profiles.
type ComponentImages struct {
	NodeAgentImage        string
	NodeExporterImage     string
	VMAgentImage          string
	KubeStateMetricsImage string
}

// WithComponentImages supplies the exact Profile images for a Kubernetes
// cluster reconciliation.
func (m *MetricsComponent) WithComponentImages(images ComponentImages) *MetricsComponent {
	m.componentImages = images

	return m
}

// WithInferenceScrapeRules attaches the per-engine-version scrape adjustments
// derived from engine metrics-export declarations. Optional: the zero value
// renders the inference job exactly as it was before engines could declare
// capabilities, which is also what the delete path wants.
func (m *MetricsComponent) WithInferenceScrapeRules(rules InferenceScrapeRules) *MetricsComponent {
	m.inferenceScrapeRules = rules

	return m
}

func NewMetricsComponent(cluster *v1.Cluster, namespace, imagePrefix, imagePullSecret, metricsRemoteWriteURL string,
	config v1.KubernetesClusterConfig, ctrlClient client.Client,
	acceleratorMgr accelerator.Manager) *MetricsComponent {
	logger := klog.LoggerWithValues(klog.Background(),
		"cluster", cluster.Metadata.WorkspaceName(),
		"component", "metrics",
	)

	return &MetricsComponent{
		cluster:               cluster,
		namespace:             namespace,
		imagePrefix:           imagePrefix,
		metricsRemoteWriteURL: metricsRemoteWriteURL,
		imagePullSecret:       imagePullSecret,
		acceleratorMgr:        acceleratorMgr,
		config:                config,
		ctrlClient:            ctrlClient,
		logger:                logger,
	}
}

// Reconcile ensures the metrics component is set up in the cluster
func (m *MetricsComponent) Reconcile() error {
	err := m.ApplyResources(context.Background())
	if err != nil {
		return errors.Wrap(err, "failed to apply metrics resources")
	}

	status, err := m.CheckResourcesStatus(context.Background())
	if err != nil {
		return errors.Wrap(err, "failed to check metrics resources status")
	}

	if status.Ready() {
		// All resources are ready
		return nil
	}

	return fmt.Errorf("metrics component is not fully ready, please check the status: %s", status.String())
}

func (m *MetricsComponent) Delete() error {
	ctx := context.Background()

	deleted, err := m.DeleteResources(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to delete metrics resources")
	}

	if err := m.cleanupNodeAnnotations(ctx); err != nil {
		return errors.Wrap(err, "failed to cleanup metrics node annotations")
	}

	if !deleted {
		return fmt.Errorf("metrics resources are not fully deleted, please wait")
	}

	return nil
}
