package metrics

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
)

// MetricsStatus represents the status of metrics component resources
type MetricsStatus struct {
	DeploymentReady bool
	PodsReady       int
	TotalPods       int
	Errors          []string
}

func (m MetricsStatus) String() string {
	return fmt.Sprintf("DeploymentReady: %v, PodsReady: %d/%d, Errors: %v",
		m.DeploymentReady, m.PodsReady, m.TotalPods, m.Errors)
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

// checkDeploymentStatus checks if the deployment is ready and running the expected cluster version.
func (m *MetricsComponent) checkDeploymentStatus(ctx context.Context) (bool, int, int, error) {
	deployment := &appsv1.Deployment{}

	err := m.ctrlClient.Get(ctx, client.ObjectKey{
		Name:      "vmagent",
		Namespace: m.namespace,
	}, deployment)
	if err != nil {
		return false, 0, 0, errors.Wrap(err, "failed to get deployment")
	}

	podsReady := int(deployment.Status.ReadyReplicas)
	totalPods := int(deployment.Status.Replicas)

	if !util.IsDeploymentUpdatedAndReady(deployment) {
		return false, podsReady, totalPods, nil
	}

	// Check that all running Pods have the expected cluster version label
	expectedVersion := m.cluster.Spec.Version
	if expectedVersion != "" {
		podList := &corev1.PodList{}

		err = m.ctrlClient.List(ctx, podList,
			client.InNamespace(m.namespace),
			client.MatchingLabels{"app": "vmagent", "cluster": m.cluster.GetName(), "workspace": m.cluster.GetWorkspace()},
		)
		if err != nil {
			return false, podsReady, totalPods, errors.Wrap(err, "failed to list vmagent pods")
		}

		for i := range podList.Items {
			pod := &podList.Items[i]
			if pod.Status.Phase != corev1.PodRunning {
				continue
			}

			if v1.GetVersionFromLabels(pod.Labels) != expectedVersion {
				return false, podsReady, totalPods, nil
			}
		}
	}

	return true, podsReady, totalPods, nil
}
