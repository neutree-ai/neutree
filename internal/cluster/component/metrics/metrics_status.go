package metrics

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// MetricsStatus represents the status of metrics component resources
type MetricsStatus struct {
	DeploymentReady bool
	PodsReady       int
	TotalPods       int
	Errors          []string
}

// CheckResourcesStatus checks the status of all metrics resources
func (m *MetricsComponent) CheckResourcesStatus(ctx context.Context) (*MetricsStatus, error) {
	status := &MetricsStatus{
		Errors: []string{},
	}

	// Check Deployment status
	deploymentReady, podsReady, totalPods, err := m.checkDeploymentStatus(ctx)
	if err != nil {
		status.Errors = append(status.Errors, fmt.Sprintf("deployment check failed: %v", err))
	} else {
		status.DeploymentReady = deploymentReady
		status.PodsReady = podsReady
		status.TotalPods = totalPods
	}

	return status, nil
}

// checkDeploymentStatus checks if the deployment is ready
func (m *MetricsComponent) checkDeploymentStatus(ctx context.Context) (bool, int, int, error) {
	deployment := &appsv1.Deployment{}
	err := m.ctrlClient.Get(ctx, client.ObjectKey{
		Name:      "vmagent",
		Namespace: m.namespace,
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
