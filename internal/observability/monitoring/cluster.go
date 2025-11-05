package monitoring

import (
	"fmt"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
)

type clusterMonitor struct {
	cluster *v1.Cluster
}

func NewClusterMonitor(cluster *v1.Cluster) ServiceMonitor {
	return &clusterMonitor{
		cluster: cluster,
	}
}

func (cm *clusterMonitor) GetMetricsScrapeTargetsConfig() ([]v1.MetricsScrapeTargetsConfig, error) {
	var (
		metricsScrapeTargetsConfigs []v1.MetricsScrapeTargetsConfig
	)

	// current only support ray cluster
	metricsScrapeTargetsConfig, err := generateRayClusterMetricsScrapeTargetsConfig(cm.cluster)
	if err != nil {
		return nil, errors.Wrap(err, "failed to generate ray metrics scrape targets config")
	}

	metricsScrapeTargetsConfigs = append(metricsScrapeTargetsConfigs, *metricsScrapeTargetsConfig)

	return metricsScrapeTargetsConfigs, nil
}

func generateRayClusterMetricsScrapeTargetsConfig(cluster *v1.Cluster) (*v1.MetricsScrapeTargetsConfig, error) {
	dashboardService := dashboard.NewDashboardService(cluster.Status.DashboardURL)

	nodes, err := dashboardService.ListNodes()
	if err != nil {
		return nil, errors.Wrap(err, "failed to list ray nodes")
	}

	metricsScrapeTargetConfig := &v1.MetricsScrapeTargetsConfig{
		Labels: map[string]string{
			"ray_io_cluster":  cluster.Metadata.Name,
			"job":             "ray",
			"neutree_cluster": cluster.Metadata.Name,
			"workspace":       cluster.Metadata.Workspace,
		},
	}

	for _, node := range nodes {
		if node.Raylet.IsHeadNode {
			metricsScrapeTargetConfig.Targets = append(metricsScrapeTargetConfig.Targets, fmt.Sprintf("%s:%d", node.IP, v1.DashboardMetricsPort))
			metricsScrapeTargetConfig.Targets = append(metricsScrapeTargetConfig.Targets, fmt.Sprintf("%s:%d", node.IP, v1.AutoScaleMetricsPort))
			metricsScrapeTargetConfig.Targets = append(metricsScrapeTargetConfig.Targets, fmt.Sprintf("%s:%d", node.IP, v1.RayletMetricsPort))

			continue
		}

		if node.Raylet.State == v1.AliveNodeState {
			metricsScrapeTargetConfig.Targets = append(metricsScrapeTargetConfig.Targets, fmt.Sprintf("%s:%d", node.IP, v1.RayletMetricsPort))
		}
	}

	return metricsScrapeTargetConfig, nil
}
