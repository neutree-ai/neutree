package cluster

import (
	"strings"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
	"github.com/neutree-ai/neutree/internal/semver"
)

func setClusterStatus(cluster *v1.Cluster, status *v1.RayClusterStatus) {
	cluster.Status.DesiredNodes = status.DesireNodes
	cluster.Status.ReadyNodes = status.ReadyNodes
	cluster.Status.RayVersion = status.RayVersion
	cluster.Status.Version = status.NeutreeServeVersion
	cluster.Status.ResourceInfo = status.ResourceInfo
}

func getRayClusterStatus(dashboardService dashboard.DashboardService) (*v1.RayClusterStatus, error) {
	clusterStatus := &v1.RayClusterStatus{}

	nodes, err := dashboardService.ListNodes()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get ray nodes")
	}

	var (
		readyNodes            int
		neutreeServingVersion string
	)

	var (
		resourceInfo = map[string]float64{}
	)

	addNodeResource := func(node v1.Raylet) {
		for resource, value := range node.Resources {
			if strings.HasPrefix(resource, "node:") {
				continue
			}

			if _, ok := resourceInfo[resource]; !ok {
				resourceInfo[resource] = 0
			}

			resourceInfo[resource] += value
		}
	}

	for _, node := range nodes {
		// skip dead nodes
		if node.Raylet.State != v1.AliveNodeState {
			continue
		}

		readyNodes++

		addNodeResource(node.Raylet)

		if _, ok := node.Raylet.Labels[v1.NeutreeServingVersionLabel]; !ok {
			continue
		}

		if neutreeServingVersion == "" {
			neutreeServingVersion = node.Raylet.Labels[v1.NeutreeServingVersionLabel]
		} else {
			var less bool

			less, err = semver.LessThan(neutreeServingVersion, node.Raylet.Labels[v1.NeutreeServingVersionLabel])
			if err != nil {
				return nil, errors.Wrap(err, "failed to compare neutree serving version")
			}

			if less {
				neutreeServingVersion = node.Raylet.Labels[v1.NeutreeServingVersionLabel]
			}
		}
	}

	clusterStatus.ReadyNodes = readyNodes
	clusterStatus.NeutreeServeVersion = neutreeServingVersion

	autoScaleStatus, err := dashboardService.GetClusterAutoScaleStatus()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get cluster autoScale status")
	}

	var (
		currentAutoScaleActiveNodes int
		pendingLauncherNodes        int
	)

	for _, activeNodeNumber := range autoScaleStatus.ActiveNodes {
		currentAutoScaleActiveNodes += activeNodeNumber
	}

	for _, pendingLauncherNumber := range autoScaleStatus.PendingLaunches {
		pendingLauncherNodes += pendingLauncherNumber
	}

	clusterStatus.AutoScaleStatus.PendingNodes = len(autoScaleStatus.PendingNodes) + pendingLauncherNodes
	clusterStatus.AutoScaleStatus.ActiveNodes = currentAutoScaleActiveNodes
	clusterStatus.AutoScaleStatus.FailedNodes = len(autoScaleStatus.FailedNodes)

	clusterMetadata, err := dashboardService.GetClusterMetadata()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get cluster metadata")
	}

	clusterStatus.PythonVersion = clusterMetadata.Data.PythonVersion
	clusterStatus.RayVersion = clusterMetadata.Data.RayVersion
	clusterStatus.DesireNodes = +clusterStatus.AutoScaleStatus.PendingNodes +
		clusterStatus.AutoScaleStatus.ActiveNodes + clusterStatus.AutoScaleStatus.FailedNodes
	clusterStatus.ResourceInfo = resourceInfo

	return clusterStatus, nil
}
