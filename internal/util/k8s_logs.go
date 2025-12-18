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

// EndpointLogInfo returns information about available logs for an endpoint
type EndpointLogInfo struct {
	ReplicaID     string
	PodName       string
	ContainerName string
	Status        string
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
		}
		logInfos = append(logInfos, logInfo)
	}

	return logInfos, nil
}
