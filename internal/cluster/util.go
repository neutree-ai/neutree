package cluster

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/orchestrator/ray/dashboard"
	"github.com/neutree-ai/neutree/internal/semver"
	"github.com/neutree-ai/neutree/pkg/storage"
)

const (
	defaultWorkdir             = "/home/ray"
	defaultModelCacheMountPath = defaultWorkdir + "/.neutree/model-cache"
)

func getBaseImage(cluster *v1.Cluster, imageRegistry *v1.ImageRegistry) (string, error) {
	registryURL, err := url.Parse(imageRegistry.Spec.URL)
	if err != nil {
		return "", errors.Wrap(err, "failed to parse image registry url "+imageRegistry.Spec.URL)
	}

	return registryURL.Host + "/" + imageRegistry.Spec.Repository + "/neutree-serve:" + cluster.Spec.Version, nil
}

func ParseSSHClusterConfig(cluster *v1.Cluster) (*v1.RaySSHProvisionClusterConfig, error) {
	if cluster.Spec.Config == nil {
		return nil, errors.New("cluster config is empty")
	}

	config := cluster.Spec.Config

	configString, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}

	sshClusterConfig := &v1.RaySSHProvisionClusterConfig{}

	err = json.Unmarshal(configString, sshClusterConfig)
	if err != nil {
		return nil, err
	}

	return sshClusterConfig, nil
}

func ParseKubernetesClusterConfig(cluster *v1.Cluster) (*v1.RayKubernetesProvisionClusterConfig, error) {
	if cluster.Spec.Config == nil {
		return nil, errors.New("cluster config is empty")
	}

	config := cluster.Spec.Config

	configString, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}

	kubernetesClusterConfig := &v1.RayKubernetesProvisionClusterConfig{}

	err = json.Unmarshal(configString, kubernetesClusterConfig)
	if err != nil {
		return nil, err
	}

	return kubernetesClusterConfig, nil
}

func getRelatedImageRegistry(s storage.Storage, cluster *v1.Cluster) (*v1.ImageRegistry, error) {
	imageRegistryFilter := []storage.Filter{
		{
			Column:   "metadata->name",
			Operator: "eq",
			Value:    fmt.Sprintf(`"%s"`, cluster.Spec.ImageRegistry),
		},
	}

	if cluster.Metadata.Workspace != "" {
		imageRegistryFilter = append(imageRegistryFilter, storage.Filter{
			Column:   "metadata->workspace",
			Operator: "eq",
			Value:    fmt.Sprintf(`"%s"`, cluster.Metadata.Workspace),
		})
	}

	imageRegistryList, err := s.ListImageRegistry(storage.ListOption{Filters: imageRegistryFilter})
	if err != nil {
		return nil, errors.Wrap(err, "failed to list image registry")
	}

	if len(imageRegistryList) == 0 {
		return nil, errors.New("relate image registry not found")
	}

	return &imageRegistryList[0], nil
}

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
