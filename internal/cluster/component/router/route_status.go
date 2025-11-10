package router

import (
	"context"
	"fmt"
	"net/url"

	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/neutree-ai/neutree/internal/util"
)

// RouteStatus represents the status of route component resources
type RouteStatus struct {
	DeploymentReady bool
	ServiceReady    bool
	PodsReady       int
	TotalPods       int
	LoadBalancerIP  string
	Errors          []string
}

func (r RouteStatus) String() string {
	return fmt.Sprintf("DeploymentReady: %v, ServiceReady: %v, PodsReady: %d/%d, LoadBalancerIP: %s, Errors: %v",
		r.DeploymentReady, r.ServiceReady, r.PodsReady, r.TotalPods, r.LoadBalancerIP, r.Errors)
}

// CheckResourcesStatus checks the status of all route resources
func (r *RouterComponent) CheckResourcesStatus(ctx context.Context) (*RouteStatus, error) {
	status := &RouteStatus{
		Errors: []string{},
	}

	// Check Deployment status
	deploymentReady, podsReady, totalPods, err := r.checkDeploymentStatus(ctx)
	if err != nil {
		status.Errors = append(status.Errors, fmt.Sprintf("deployment check failed: %v", err))
	} else {
		status.DeploymentReady = deploymentReady
		status.PodsReady = podsReady
		status.TotalPods = totalPods
	}

	// Check Service status
	serviceReady, lbIP, err := r.checkServiceStatus(ctx)
	if err != nil {
		status.Errors = append(status.Errors, fmt.Sprintf("service check failed: %v", err))
	} else {
		status.ServiceReady = serviceReady
		status.LoadBalancerIP = lbIP
	}

	return status, nil
}

// checkDeploymentStatus checks if the deployment is ready
func (r *RouterComponent) checkDeploymentStatus(ctx context.Context) (bool, int, int, error) {
	deployment := &appsv1.Deployment{}

	err := r.ctrlClient.Get(ctx, client.ObjectKey{
		Name:      "router",
		Namespace: r.namespace,
	}, deployment)
	if err != nil {
		return false, 0, 0, errors.Wrap(err, "failed to get deployment")
	}

	readyReplicas := int(deployment.Status.ReadyReplicas)
	curReplicas := int(deployment.Status.Replicas)

	return util.IsDeploymentUpdatedAndReady(deployment), readyReplicas, curReplicas, nil
}

// checkServiceStatus checks if the service is ready and has LoadBalancer IP
func (r *RouterComponent) checkServiceStatus(ctx context.Context) (bool, string, error) {
	service := &corev1.Service{}

	err := r.ctrlClient.Get(ctx, client.ObjectKey{
		Name:      "router-service",
		Namespace: r.namespace,
	}, service)
	if err != nil {
		return false, "", errors.Wrap(err, "failed to get service")
	}

	// Check if service has LoadBalancer IP
	if service.Spec.Type == corev1.ServiceTypeLoadBalancer {
		if len(service.Status.LoadBalancer.Ingress) == 0 {
			return false, "", nil
		}

		// Get IP or Hostname
		if service.Status.LoadBalancer.Ingress[0].IP != "" {
			return true, service.Status.LoadBalancer.Ingress[0].IP, nil
		}

		if service.Status.LoadBalancer.Ingress[0].Hostname != "" {
			return true, service.Status.LoadBalancer.Ingress[0].Hostname, nil
		}
	}

	return true, "", nil
}

// GetRouteEndpoint returns the accessible endpoint for the router service
func (r *RouterComponent) GetRouteEndpoint(ctx context.Context) (string, error) {
	service := &corev1.Service{}

	err := r.ctrlClient.Get(ctx, client.ObjectKey{
		Name:      "router-service",
		Namespace: r.namespace,
	}, service)
	if err != nil {
		return "", errors.Wrap(err, "failed to get service")
	}

	// For LoadBalancer type
	if service.Spec.Type == corev1.ServiceTypeLoadBalancer {
		if len(service.Status.LoadBalancer.Ingress) > 0 {
			ingress := service.Status.LoadBalancer.Ingress[0]
			if ingress.IP != "" {
				return fmt.Sprintf("http://%s:%d", ingress.IP, service.Spec.Ports[0].Port), nil
			}

			if ingress.Hostname != "" {
				return fmt.Sprintf("http://%s:%d", ingress.Hostname, service.Spec.Ports[0].Port), nil
			}
		}

		return "", errors.New("LoadBalancer service has no ingress IP/Hostname")
	}

	// For NodePort, we currently do not support scenarios where the API server access address is LoadBalancer.
	// If LoadBalancer is supported, it is recommended to configure AccessMode to LoadBalancer.
	if service.Spec.Type == corev1.ServiceTypeNodePort {
		// check NodePort already assigned
		if len(service.Spec.Ports) == 0 || service.Spec.Ports[0].NodePort == 0 {
			return "", errors.New("NodePort service has no node port assigned")
		}

		// get apiserver url from kubeconfig
		apiserverUrl, err := util.GetApiServerUrlFromKubeConfig(r.config.Kubeconfig)
		if err != nil {
			return "", errors.Wrap(err, "failed to get apiserver url from kubeconfig")
		}

		parsedUrl, err := url.Parse(apiserverUrl)
		if err != nil {
			return "", errors.Wrap(err, "failed to parse apiserver url")
		}

		return fmt.Sprintf("http://%s:%d", parsedUrl.Hostname(), service.Spec.Ports[0].NodePort), nil
	}

	return "", errors.Errorf("unsupported service type: %s", service.Spec.Type)
}
