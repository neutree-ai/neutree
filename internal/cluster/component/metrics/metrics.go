package metrics

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

const (
	metricsLastAppliedConfigAnnotation = "metrics." + v1.AnnotationLastAppliedConfig
)

type MetricsComponent struct {
	cluster               *v1.Cluster
	namespace             string
	imagePrefix           string
	metricsRemoteWriteURL string
	imagePullSecret       string

	config     v1.KubernetesClusterConfig
	ctrlClient client.Client
	logger     klog.Logger
}

func NewMetricsComponent(cluster *v1.Cluster, namespace, imagePrefix, imagePullSecret, metricsRemoteWriteURL string,
	config v1.KubernetesClusterConfig, ctrlClient client.Client) *MetricsComponent {
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

	if status.DeploymentReady {
		// All resources are ready
		return nil
	}

	return fmt.Errorf("metrics component is not fully ready, please check the status: %s", status.String())
}

func (m *MetricsComponent) Delete() error {
	// Implement the logic to delete the metrics component from the cluster
	deleted, err := m.DeleteResources(context.Background())
	if err != nil {
		return errors.Wrap(err, "failed to delete metrics resources")
	}

	if !deleted {
		return fmt.Errorf("metrics resources are not fully deleted, please wait")
	}

	return nil
}
