package router

import (
	"context"
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/pkg/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type RouterComponent struct {
	clusterName     string
	workspace       string
	namespace       string
	imagePrefix     string
	imagePullSecret string
	config          v1.KubernetesClusterConfig
	ctrlClient      client.Client
}

// RouteManifestData holds the data for rendering route manifests
type RouteManifestData struct {
	ClusterName     string
	Workspace       string
	Namespace       string
	ImagePrefix     string
	ImagePullSecret string
	Version         string
	Replicas        int
	Resources       map[string]string
}

func NewRouterComponent(clusterName, clusterWs, namespace, imagePrefix, imagePullSecret string, config v1.KubernetesClusterConfig, ctrlClient client.Client) *RouterComponent {
	return &RouterComponent{
		clusterName:     clusterName,
		workspace:       clusterWs,
		namespace:       namespace,
		imagePrefix:     imagePrefix,
		imagePullSecret: imagePullSecret,
		config:          config,
		ctrlClient:      ctrlClient,
	}
}

// Reconcile ensures the route component is set up in the cluster
func (r *RouterComponent) Reconcile() error {
	err := r.ApplyResources(context.Background())
	if err != nil {
		return errors.Wrap(err, "failed to apply route resources")
	}

	status, err := r.CheckResourcesStatus(context.Background()) // Log status, but ignore error for now
	if err != nil {
		return errors.Wrap(err, "failed to check route resources status")
	}

	if status.DeploymentReady && status.ServiceReady {
		// All resources are ready
		return nil
	}

	return fmt.Errorf("route component is not fully ready, please check the status: %v", status)
}

func (r *RouterComponent) Delete() error {
	// Implement the logic to delete the route component from the cluster
	// This may involve deleting Kubernetes resources like Services, Deployments, etc.
	deleted, err := r.DeleteResources(context.Background())
	if err != nil {
		return errors.Wrap(err, "failed to delete route resources")
	}

	if !deleted {
		return fmt.Errorf("route resources are not fully deleted, please wait")
	}

	return nil
}
