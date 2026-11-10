package metrics

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator/operator"
	publicaccelerator "github.com/neutree-ai/neutree/pkg/accelerator"
	"github.com/neutree-ai/neutree/pkg/clustercache"
	"github.com/neutree-ai/neutree/pkg/metricsconfig"
)

type MetricsComponent struct {
	cluster               *v1.Cluster
	namespace             string
	imagePrefix           string
	metricsRemoteWriteURL string
	imagePullSecret       string
	acceleratorMgr        publicaccelerator.ProfileProvider
	excludedAccelerators  map[string]struct{}
	acceleratorOperators  map[string]operator.AcceleratorOperator
	acceleratorOnly       string
	metricsTargets        metricsconfig.Registry

	config     v1.KubernetesClusterConfig
	ctrlClient client.Client
	logger     klog.Logger

	inferenceScrapeRules InferenceScrapeRules
	cacheProviders       *clustercache.Providers
}

// WithInferenceScrapeRules attaches the per-engine-version scrape adjustments
// derived from engine metrics-export declarations. Optional: the zero value
// renders the inference job exactly as it was before engines could declare
// capabilities, which is also what the delete path wants.
func (m *MetricsComponent) WithInferenceScrapeRules(rules InferenceScrapeRules) *MetricsComponent {
	m.inferenceScrapeRules = rules

	return m
}

// WithCacheProviders supplies distribution-owned cache metrics discovery.
func (m *MetricsComponent) WithCacheProviders(providers *clustercache.Providers) *MetricsComponent {
	m.cacheProviders = providers
	return m
}

// WithExcludedAccelerators prevents the legacy profile renderer from
// generating resources owned by an AcceleratorOperator. Operator-owned
// accelerators are rendered and reconciled by their operator instead.
func (m *MetricsComponent) WithExcludedAccelerators(accelerators map[string]struct{}) *MetricsComponent {
	m.excludedAccelerators = make(map[string]struct{}, len(accelerators)+len(m.acceleratorOperators))
	for acceleratorType := range accelerators {
		m.excludedAccelerators[acceleratorType] = struct{}{}
	}

	for _, acceleratorOperator := range m.acceleratorOperators {
		if acceleratorOperator != nil {
			m.excludedAccelerators[acceleratorOperator.AcceleratorType()] = struct{}{}
		}
	}

	return m
}

// WithAcceleratorOperators supplies the lifecycle owners already selected by
// the cluster. Metrics only reads their declarations; it never reconciles them.
func (m *MetricsComponent) WithAcceleratorOperators(operators map[string]operator.AcceleratorOperator) *MetricsComponent {
	m.acceleratorOperators = make(map[string]operator.AcceleratorOperator, len(operators))
	for acceleratorType, acceleratorOperator := range operators {
		m.acceleratorOperators[acceleratorType] = acceleratorOperator
	}

	return m.WithExcludedAccelerators(m.excludedAccelerators)
}

// WithAcceleratorOnly restricts rendering to one accelerator's NodeAgent and
// exporter resources. It is used by an AcceleratorOperator renderer; the
// ordinary MetricsComponent path remains unchanged.
func (m *MetricsComponent) WithAcceleratorOnly(acceleratorType string) *MetricsComponent {
	m.acceleratorOnly = acceleratorType

	return m
}

func (m *MetricsComponent) WithMetricsTargets(registry metricsconfig.Registry) *MetricsComponent {
	m.metricsTargets = registry

	return m
}

func NewMetricsComponent(cluster *v1.Cluster, namespace, imagePrefix, imagePullSecret, metricsRemoteWriteURL string,
	config v1.KubernetesClusterConfig, ctrlClient client.Client,
	acceleratorMgr publicaccelerator.ProfileProvider) *MetricsComponent {
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

	nodes, err := m.nodeAnnotationCleanupScope(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to determine metrics annotation scope")
	}

	deleted, err := m.DeleteResources(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to delete metrics resources")
	}

	if err := m.cleanupNodeAnnotations(ctx, nodes); err != nil {
		return errors.Wrap(err, "failed to cleanup metrics node annotations")
	}

	if !deleted {
		return fmt.Errorf("metrics resources are not fully deleted, please wait")
	}

	return nil
}
