package metrics

import (
	"context"
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/pkg/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type MetricsComponent struct {
	clusterName           string
	workspace             string
	namespace             string
	imagePrefix           string
	metricsRemoteWriteURL string
	imagePullSecret       string

	config     v1.KubernetesClusterConfig
	ctrlClient client.Client
}

// MetricsManifestData holds the data for rendering metrics manifests
type MetricsManifestData struct {
	ClusterName           string
	Workspace             string
	Namespace             string
	ImagePrefix           string
	ImagePullSecret       string
	Version               string
	MetricsRemoteWriteURL string
	Replicas              int
	Resources             map[string]string
}

func NewMetricsComponent(clusterName, clusterWs, namespace, imagePrefix, imagePullSecret,metricsRemoteWriteURL string,
	config v1.KubernetesClusterConfig, ctrlClient client.Client) *MetricsComponent {
	return &MetricsComponent{
		clusterName:           clusterName,
		workspace:             clusterWs,
		namespace:             namespace,
		imagePrefix:           imagePrefix,
		metricsRemoteWriteURL: metricsRemoteWriteURL,
		imagePullSecret:       imagePullSecret,
		config:                config,
		ctrlClient:            ctrlClient,
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

	return fmt.Errorf("metrics component is not fully ready, please check the status: %v", status)
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
