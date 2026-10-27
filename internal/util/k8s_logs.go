package util

import (
	"context"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// GetEndpointPods retrieves all pods for a given endpoint in a Kubernetes cluster
func GetEndpointPods(ctx context.Context, cluster *v1.Cluster, endpoint *v1.Endpoint) ([]corev1.Pod, error) {
	ctrlClient, err := GetClientFromCluster(cluster)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get kubernetes client for cluster %s", cluster.Metadata.Name)
	}

	namespace := ClusterNamespace(cluster)

	// Build label selector to find endpoint pods
	labelSelector := labels.SelectorFromSet(labels.Set{
		"workspace": endpoint.Metadata.Workspace,
		"endpoint":  endpoint.Metadata.Name,
		"app":       "inference",
	})

	podList := &corev1.PodList{}

	err = ctrlClient.List(ctx, podList, &client.ListOptions{
		Namespace:     namespace,
		LabelSelector: labelSelector,
	})
	if err != nil {
		return nil, errors.Wrapf(err, "failed to list pods for endpoint %s", endpoint.Metadata.Name)
	}

	return podList.Items, nil
}

type ContainerLogInfo struct {
	Name         string
	RestartCount int32
	Started      bool
}

// EndpointLogInfo returns information about available logs for an endpoint
type EndpointLogInfo struct {
	ReplicaID     string
	PodName       string
	ContainerName string
	Status        string
	Containers    []ContainerLogInfo
}

func GetPodLogContainers(pod *corev1.Pod) []ContainerLogInfo {
	containerStatuses := make(map[string]corev1.ContainerStatus, len(pod.Status.ContainerStatuses))
	for _, status := range pod.Status.ContainerStatuses {
		containerStatuses[status.Name] = status
	}

	initStatuses := make(map[string]corev1.ContainerStatus, len(pod.Status.InitContainerStatuses))
	for _, status := range pod.Status.InitContainerStatuses {
		initStatuses[status.Name] = status
	}

	containers := make([]ContainerLogInfo, 0, len(pod.Spec.Containers)+len(pod.Spec.InitContainers))

	for _, container := range pod.Spec.Containers {
		status := containerStatuses[container.Name]
		containers = append(containers, ContainerLogInfo{
			Name:         container.Name,
			RestartCount: status.RestartCount,
			Started: status.State.Running != nil || status.State.Terminated != nil ||
				status.LastTerminationState.Terminated != nil || status.RestartCount > 0,
		})
	}

	for _, container := range pod.Spec.InitContainers {
		status := initStatuses[container.Name]
		containers = append(containers, ContainerLogInfo{
			Name:         container.Name,
			RestartCount: status.RestartCount,
			Started: status.State.Running != nil || status.State.Terminated != nil ||
				status.LastTerminationState.Terminated != nil || status.RestartCount > 0,
		})
	}

	return containers
}

// GetEndpointLogsInfo retrieves log information for all replicas of an endpoint
func GetEndpointLogsInfo(ctx context.Context, cluster *v1.Cluster, endpoint *v1.Endpoint) ([]EndpointLogInfo, error) {
	pods, err := GetEndpointPods(ctx, cluster, endpoint)
	if err != nil {
		return nil, err
	}

	var logInfos []EndpointLogInfo

	for _, pod := range pods {
		// Get main container name (first non-init container)
		var containerName string
		if len(pod.Spec.Containers) > 0 {
			containerName = pod.Spec.Containers[0].Name
		}

		logInfo := EndpointLogInfo{
			ReplicaID:     pod.Name,
			PodName:       pod.Name,
			ContainerName: containerName,
			Status:        string(pod.Status.Phase),
			Containers:    GetPodLogContainers(&pod),
		}
		logInfos = append(logInfos, logInfo)
	}

	return logInfos, nil
}
