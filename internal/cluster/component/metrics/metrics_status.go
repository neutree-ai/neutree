package metrics

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/neutree-ai/neutree/internal/cluster/component"
	"github.com/neutree-ai/neutree/internal/util"
)

// MetricsStatus represents the status of metrics component resources
type MetricsStatus struct {
	DeploymentReady                 bool
	KubeStateMetricsRequired        bool
	KubeStateMetricsDeploymentReady bool
	PodsReady                       int
	TotalPods                       int
	KubeStateMetricsPodsReady       int
	KubeStateMetricsTotalPods       int
	Errors                          []string
	Diagnostics                     []string
}

func (m MetricsStatus) String() string {
	base := fmt.Sprintf(
		"DeploymentReady: %v, PodsReady: %d/%d, KubeStateMetricsRequired: %v, "+
			"KubeStateMetricsDeploymentReady: %v, KubeStateMetricsPodsReady: %d/%d, Errors: %v",
		m.DeploymentReady, m.PodsReady, m.TotalPods,
		m.KubeStateMetricsRequired,
		m.KubeStateMetricsDeploymentReady, m.KubeStateMetricsPodsReady, m.KubeStateMetricsTotalPods,
		m.Errors)

	return component.FormatStatusWithDiagnostics(base, m.Diagnostics)
}

func (m MetricsStatus) Ready() bool {
	if len(m.Errors) > 0 {
		return false
	}

	return m.DeploymentReady && (!m.KubeStateMetricsRequired || m.KubeStateMetricsDeploymentReady)
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
		status.Diagnostics = append(status.Diagnostics, component.DeploymentDiagnostics(ctx, m.ctrlClient, m.namespace, "vmagent", m.metricsPodLabels())...)
	} else {
		status.DeploymentReady = deploymentReady
		status.PodsReady = podsReady
		status.TotalPods = totalPods

		if !deploymentReady {
			status.Diagnostics = append(status.Diagnostics, component.DeploymentDiagnostics(ctx, m.ctrlClient, m.namespace, "vmagent", m.metricsPodLabels())...)
		}
	}

	kubeStateMetricsRequired, err := m.supportsKubeStateMetrics()
	if err != nil {
		status.Errors = append(status.Errors, fmt.Sprintf("kube-state-metrics support check failed: %v", err))
		return status, nil
	}

	status.KubeStateMetricsRequired = kubeStateMetricsRequired

	if !kubeStateMetricsRequired {
		// Older cluster releases do not render the extra kube-state-metrics
		// deployment, so vmagent readiness is the complete metrics status.
		return status, nil
	}

	kubeStateMetricsReady, kubeStateMetricsPodsReady, kubeStateMetricsTotalPods, err := m.checkKubeStateMetricsDeploymentStatus(ctx)
	kubeStateMetricsDiagnostics := func() []string {
		return component.DeploymentDiagnostics(ctx, m.ctrlClient, m.namespace, "neutree-kube-state-metrics", m.kubeStateMetricsPodLabels())
	}

	if err != nil {
		status.Errors = append(status.Errors, fmt.Sprintf("kube-state-metrics deployment check failed: %v", err))
		status.Diagnostics = append(status.Diagnostics, kubeStateMetricsDiagnostics()...)
	} else {
		status.KubeStateMetricsDeploymentReady = kubeStateMetricsReady
		status.KubeStateMetricsPodsReady = kubeStateMetricsPodsReady
		status.KubeStateMetricsTotalPods = kubeStateMetricsTotalPods

		if !kubeStateMetricsReady {
			status.Diagnostics = append(status.Diagnostics, kubeStateMetricsDiagnostics()...)
		}
	}

	return status, nil
}

func (m *MetricsComponent) metricsPodLabels() map[string]string {
	return map[string]string{"app": "vmagent", "cluster": m.cluster.GetName(), "workspace": m.cluster.GetWorkspace()}
}

func (m *MetricsComponent) kubeStateMetricsPodLabels() map[string]string {
	return map[string]string{
		"app":       "neutree-kube-state-metrics",
		"cluster":   m.cluster.GetName(),
		"workspace": m.cluster.GetWorkspace(),
	}
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
	match, err := component.AllPodsMatchVersion(ctx, m.ctrlClient, m.namespace,
		m.metricsPodLabels(),
		m.cluster.GetVersion())
	if err != nil {
		return false, podsReady, totalPods, err
	}

	return match, podsReady, totalPods, nil
}

func (m *MetricsComponent) checkKubeStateMetricsDeploymentStatus(ctx context.Context) (bool, int, int, error) {
	return m.checkNamedDeploymentStatus(ctx, "neutree-kube-state-metrics", m.kubeStateMetricsPodLabels())
}

func (m *MetricsComponent) checkNamedDeploymentStatus(ctx context.Context, name string, matchLabels map[string]string) (bool, int, int, error) {
	deployment := &appsv1.Deployment{}

	err := m.ctrlClient.Get(ctx, client.ObjectKey{
		Name:      name,
		Namespace: m.namespace,
	}, deployment)
	if err != nil {
		return false, 0, 0, errors.Wrapf(err, "failed to get deployment %s", name)
	}

	podsReady := int(deployment.Status.ReadyReplicas)
	totalPods := int(deployment.Status.Replicas)

	if !util.IsDeploymentUpdatedAndReady(deployment) {
		return false, podsReady, totalPods, nil
	}

	match, err := component.AllPodsMatchVersion(ctx, m.ctrlClient, m.namespace, matchLabels, m.cluster.GetVersion())
	if err != nil {
		return false, podsReady, totalPods, err
	}

	return match, podsReady, totalPods, nil
}
