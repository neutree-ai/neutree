package router

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

const (
	routerLastAppliedConfigAnnotation = "router." + v1.AnnotationLastAppliedConfig
)

type RouterComponent struct {
	cluster         *v1.Cluster
	namespace       string
	imagePrefix     string
	imagePullSecret string
	config          v1.KubernetesClusterConfig
	ctrlClient      client.Client
	logger          klog.Logger
}

func NewRouterComponent(cluster *v1.Cluster, namespace, imagePrefix, imagePullSecret string,
	config v1.KubernetesClusterConfig, ctrlClient client.Client) *RouterComponent {
	logger := klog.LoggerWithValues(klog.Background(),
		"cluster", cluster.Metadata.WorkspaceName(),
		"component", "router",
	)

	return &RouterComponent{
		cluster:         cluster,
		namespace:       namespace,
		imagePrefix:     imagePrefix,
		imagePullSecret: imagePullSecret,
		config:          config,
		ctrlClient:      ctrlClient,
		logger:          logger,
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

	return fmt.Errorf("route component is not fully ready, please check the status: %s", status.String())
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
