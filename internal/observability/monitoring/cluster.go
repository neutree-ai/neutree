package monitoring

import (
	"fmt"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/orchestrator"
)

type clusterMonitor struct {
	cluster             *v1.Cluster
	clusterOrchestrator orchestrator.Orchestrator
}

func NewClusterMonitor(cluster *v1.Cluster, clusterOrchestrator orchestrator.Orchestrator) ServiceMonitor {
	return &clusterMonitor{
		cluster:             cluster,
		clusterOrchestrator: clusterOrchestrator,
	}
}

func (cm *clusterMonitor) GetMetricsScrapeTargetsConfig() ([]MetricsScrapeTargetsConfig, error) {
	var (
		metricsScrapeTargetsConfigs []MetricsScrapeTargetsConfig
	)

	// current only support ray cluster
	metricsScrapeTargetsConfig, err := generateRayClusterMetricsScrapeTargetsConfig(cm.cluster, cm.clusterOrchestrator)
	if err != nil {
		return nil, errors.Wrap(err, "failed to generate ray metrics scrape targets config")
	}

	metricsScrapeTargetsConfigs = append(metricsScrapeTargetsConfigs, *metricsScrapeTargetsConfig)

	return metricsScrapeTargetsConfigs, nil
}

func generateRayClusterMetricsScrapeTargetsConfig(cluster *v1.Cluster, clusterOrchestrator orchestrator.Orchestrator) (*MetricsScrapeTargetsConfig, error) {
	nodes, err := clusterOrchestrator.ListNodes()
	if err != nil {
		return nil, errors.Wrap(err, "failed to list ray nodes")
	}

	metricsScrapeTargetConfig := &MetricsScrapeTargetsConfig{
		Labels: map[string]string{
			"ray_io_cluster": cluster.Metadata.Name,
			"job":            "ray",
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
