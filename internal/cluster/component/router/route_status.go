package router

import (
	"context"
	"fmt"
	"time"

	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
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

	// Check deployment conditions
	ready := false
	for _, condition := range deployment.Status.Conditions {
		if condition.Type == appsv1.DeploymentAvailable && condition.Status == corev1.ConditionTrue {
			ready = true
			break
		}
	}

	podsReady := int(deployment.Status.ReadyReplicas)
	totalPods := int(deployment.Status.Replicas)

	return ready, podsReady, totalPods, nil
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

// WaitForResourcesReady waits for all route resources to be ready
func (r *RouterComponent) WaitForResourcesReady(ctx context.Context, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		status, err := r.CheckResourcesStatus(ctx)
		if err != nil {
			return false, err
		}

		// Check if there are any errors
		if len(status.Errors) > 0 {
			return false, nil
		}

		// Check if deployment is ready
		if !status.DeploymentReady {
			return false, nil
		}

		// Check if all pods are ready
		if status.PodsReady < status.TotalPods {
			return false, nil
		}

		// Check if service is ready
		if !status.ServiceReady {
			return false, nil
		}

		// All checks passed
		return true, nil
	})
}

// WaitForDeploymentReady waits only for deployment to be ready
func (r *RouterComponent) WaitForDeploymentReady(ctx context.Context, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		ready, podsReady, totalPods, err := r.checkDeploymentStatus(ctx)
		if err != nil {
			return false, err
		}

		return ready && podsReady == totalPods && totalPods > 0, nil
	})
}

// WaitForServiceLoadBalancer waits for service to get LoadBalancer IP/Hostname
func (r *RouterComponent) WaitForServiceLoadBalancer(ctx context.Context, timeout time.Duration) (string, error) {
	var lbAddress string

	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		ready, ip, err := r.checkServiceStatus(ctx)
		if err != nil {
			return false, err
		}

		if ready && ip != "" {
			lbAddress = ip
			return true, nil
		}

		return false, nil
	})

	if err != nil {
		return "", err
	}

	return lbAddress, nil
}

// GetReadyPods returns list of ready pods for the router
func (r *RouterComponent) GetReadyPods(ctx context.Context) ([]corev1.Pod, error) {
	podList := &corev1.PodList{}

	err := r.ctrlClient.List(ctx, podList,
		client.InNamespace(r.namespace),
		client.MatchingLabels{
			"app":       "router",
			"cluster":   r.clusterName,
			"workspace": r.workspace,
		})
	if err != nil {
		return nil, errors.Wrap(err, "failed to list pods")
	}

	var readyPods []corev1.Pod
	for _, pod := range podList.Items {
		if pod.Status.Phase == corev1.PodRunning && isPodReady(&pod) {
			readyPods = append(readyPods, pod)
		}
	}

	return readyPods, nil
}

// isPodReady checks if a pod is ready based on its conditions
func isPodReady(pod *corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
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
				return ingress.IP, nil
			}
			if ingress.Hostname != "" {
				return ingress.Hostname, nil
			}
		}
		return "", errors.New("LoadBalancer service has no ingress IP/Hostname")
	}

	// For ClusterIP type (for internal access)
	if service.Spec.Type == corev1.ServiceTypeClusterIP {
		return fmt.Sprintf("http://%s.%s.svc.cluster.local", service.Name, service.Namespace), nil
	}

	return "", errors.Errorf("unsupported service type: %s", service.Spec.Type)
}
