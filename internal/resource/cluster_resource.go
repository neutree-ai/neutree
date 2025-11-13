package resource

import (
	"context"
	"fmt"
	"math"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	resourceutil "k8s.io/kubectl/pkg/util/resource"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
	"github.com/neutree-ai/neutree/internal/util"
)

type clusterService struct {
	acceleratorPluginRegistry accelerator.PluginRegistry
}

func (s *clusterService) CollectClusterResources(
	ctx context.Context,
	cluster *v1.Cluster,
) (*v1.ClusterResources, error) {
	if cluster == nil {
		return nil, fmt.Errorf("cluster is nil")
	}

	var (
		resource *v1.ClusterResources
		err      error
	)

	// Determine cluster type and collect accordingly
	switch cluster.Spec.Type {
	case v1.SSHClusterType:
		resource, err = s.collectRayClusterResources(ctx, cluster)
	case v1.KubernetesClusterType:
		resource, err = s.collectK8sClusterResources(ctx, cluster)
	default:
		return nil, fmt.Errorf("unsupported cluster type: %s", cluster.Spec.Type)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to collect resources for cluster %s: %w", cluster.Metadata.Name, err)
	}

	return resource, nil
}

func (s *clusterService) collectRayClusterResources(
	_ context.Context,
	cluster *v1.Cluster,
) (*v1.ClusterResources, error) {
	// Validate DashboardURL exists
	if cluster.Status == nil || cluster.Status.DashboardURL == "" {
		return nil, fmt.Errorf("cluster dashboard URL not available in status")
	}

	dashboardURL := cluster.Status.DashboardURL

	// Create dashboard service client
	dashboardSvc := dashboard.NewDashboardService(dashboardURL)
	// Fetch node list from Dashboard
	nodeList, err := dashboardSvc.ListNodes()
	if err != nil {
		return nil, fmt.Errorf("failed to get node list from Ray Dashboard: %w", err)
	}

	availableResource := map[string]float64{}
	allocatableResource := map[string]float64{}

	for _, node := range nodeList {
		if node.Raylet.State != "ALIVE" {
			continue
		}

		nodeAvailableResource := map[string]float64{}
		nodeAllocatableResource := map[string]float64{}

		for resourceKey, quantity := range node.Raylet.Resources {
			nodeAllocatableResource[resourceKey] = quantity
			nodeAvailableResource[resourceKey] = quantity
		}

		for _, workers := range node.Raylet.CoreWorkersStats {
			for resourceKey, allocations := range workers.UsedResources {
				nodeAvailableResource[resourceKey] -= float64(allocations.TotalAllocation())
			}
		}

		for resourceKey, quantity := range nodeAvailableResource {
			availableResource[resourceKey] += quantity
		}

		for resourceKey, quantity := range nodeAllocatableResource {
			allocatableResource[resourceKey] += quantity
		}
	}

	resourceParserMap := s.acceleratorPluginRegistry.GetAllParsers()

	result := &v1.ClusterResources{
		Allocatable: &v1.ResourceInfo{
			CPU:               0,
			Memory:            0,
			AcceleratorGroups: make(map[v1.AcceleratorType]*v1.AcceleratorGroup),
		},
		Available: &v1.ResourceInfo{
			CPU:               0,
			Memory:            0,
			AcceleratorGroups: make(map[v1.AcceleratorType]*v1.AcceleratorGroup),
		},
	}

	if availableResource["CPU"] < 0 {
		availableResource["CPU"] = 0
	} else {
		availableResource["CPU"] = roundFloat64ToTwoDecimals(availableResource["CPU"])
	}

	if availableResource["memory"] < 0 {
		availableResource["memory"] = 0
	} else {
		availableResource["memory"] = roundFloat64ToTwoDecimals(availableResource["memory"])
	}

	if allocatableResource["CPU"] < 0 {
		allocatableResource["CPU"] = 0
	} else {
		allocatableResource["CPU"] = roundFloat64ToTwoDecimals(allocatableResource["CPU"])
	}

	if allocatableResource["memory"] < 0 {
		allocatableResource["memory"] = 0
	} else {
		allocatableResource["memory"] = roundFloat64ToTwoDecimals(allocatableResource["memory"])
	}

	result.Allocatable.CPU += allocatableResource["CPU"]
	result.Allocatable.Memory += allocatableResource["memory"]
	result.Available.CPU += availableResource["CPU"]
	result.Available.Memory += availableResource["memory"]

	for resourceKey, parser := range resourceParserMap {
		allocatableInfo, err := parser.ParseFromRay(allocatableResource)
		if err != nil {
			return nil, fmt.Errorf("failed to parse allocatable resources from Ray for resource %s: %w", resourceKey, err)
		}

		if allocatableInfo != nil {
			for key, group := range allocatableInfo.AcceleratorGroups {
				if existingGroup, exists := result.Allocatable.AcceleratorGroups[key]; exists {
					existingGroup.Quantity += group.Quantity
					for productKey, quantity := range group.ProductGroups {
						if existingQuantity, exists := existingGroup.ProductGroups[productKey]; exists {
							existingGroup.ProductGroups[productKey] = existingQuantity + quantity
						} else {
							existingGroup.ProductGroups[productKey] = quantity
						}
					}

					result.Allocatable.AcceleratorGroups[key] = existingGroup
				} else {
					result.Allocatable.AcceleratorGroups[key] = allocatableInfo.AcceleratorGroups[key]
				}
			}
		}

		availableInfo, err := parser.ParseFromRay(availableResource)
		if err != nil {
			return nil, fmt.Errorf("failed to parse available resources from Ray for resource %s: %w", resourceKey, err)
		}

		if availableInfo != nil {
			for key, group := range availableInfo.AcceleratorGroups {
				if existingGroup, exists := result.Available.AcceleratorGroups[key]; exists {
					existingGroup.Quantity += group.Quantity
					for productKey, quantity := range group.ProductGroups {
						if existingQuantity, exists := existingGroup.ProductGroups[productKey]; exists {
							existingGroup.ProductGroups[productKey] = existingQuantity + quantity
						} else {
							existingGroup.ProductGroups[productKey] = quantity
						}
					}

					result.Available.AcceleratorGroups[key] = existingGroup
				} else {
					result.Available.AcceleratorGroups[key] = availableInfo.AcceleratorGroups[key]
				}
			}
		}
	}

	for _, group := range result.Available.AcceleratorGroups {
		if group.Quantity < 0 {
			group.Quantity = 0
		}
	}

	return result, nil
}

func (s *clusterService) collectK8sClusterResources(
	ctx context.Context,
	cluster *v1.Cluster,
) (*v1.ClusterResources, error) {
	clientset, err := util.GetClientSetFromCluster(cluster)
	if err != nil {
		return nil, fmt.Errorf("failed to create k8s clientset: %w", err)
	}

	nodeList, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list k8s nodes: %w", err)
	}

	podList, err := clientset.CoreV1().Pods(corev1.NamespaceAll).List(ctx, metav1.ListOptions{
		FieldSelector: "status.phase!=Failed,status.phase!=Succeeded",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list k8s pods: %w", err)
	}

	type nodeResourceInfo struct {
		allocatableResource map[corev1.ResourceName]resource.Quantity
		availableResource   map[corev1.ResourceName]resource.Quantity
		labels              map[string]string
	}
	nodeResources := make(map[string]*nodeResourceInfo)

	for _, node := range nodeList.Items {
		if node.Spec.Unschedulable {
			continue
		}

		nodeInfo := &nodeResourceInfo{
			allocatableResource: make(map[corev1.ResourceName]resource.Quantity),
			availableResource:   make(map[corev1.ResourceName]resource.Quantity),
			labels:              node.Labels,
		}

		nodeInfo.allocatableResource = node.Status.Allocatable.DeepCopy()
		nodeInfo.availableResource = node.Status.Allocatable.DeepCopy()

		nodeResources[node.Name] = nodeInfo
	}

	for _, pod := range podList.Items {
		nodeName := pod.Spec.NodeName
		if nodeName == "" {
			continue
		}

		nodeInfo, exists := nodeResources[nodeName]
		if !exists {
			continue
		}

		totalRequested, _ := resourceutil.PodRequestsAndLimits(&pod)

		for resourceName, quantity := range totalRequested {
			if existingQty, exists := nodeInfo.availableResource[resourceName]; exists {
				existingQty.Sub(quantity)
				nodeInfo.availableResource[resourceName] = existingQty
			} else {
				klog.Warningf("pod %s requests unknown resource %s on node %s", pod.Name, resourceName, nodeName)
			}
		}
	}

	// Initialize result
	result := &v1.ClusterResources{
		Allocatable: &v1.ResourceInfo{
			CPU:               0,
			Memory:            0,
			AcceleratorGroups: make(map[v1.AcceleratorType]*v1.AcceleratorGroup),
		},
		Available: &v1.ResourceInfo{
			CPU:               0,
			Memory:            0,
			AcceleratorGroups: make(map[v1.AcceleratorType]*v1.AcceleratorGroup),
		},
	}

	resourceParserMap := s.acceleratorPluginRegistry.GetAllParsers()

	for nodeName, nodeInfo := range nodeResources {
		allocatableCPU := nodeInfo.allocatableResource[corev1.ResourceCPU]
		allocatableMemory := nodeInfo.allocatableResource[corev1.ResourceMemory]
		result.Allocatable.CPU += allocatableCPU.AsApproximateFloat64()
		result.Allocatable.Memory += allocatableMemory.AsApproximateFloat64()

		availableCPU := nodeInfo.availableResource[corev1.ResourceCPU]
		availableMemory := nodeInfo.availableResource[corev1.ResourceMemory]
		result.Available.CPU += availableCPU.AsApproximateFloat64()
		result.Available.Memory += availableMemory.AsApproximateFloat64()

		klog.Infof("Node %s allocatable resources: %+v", nodeName, nodeInfo.allocatableResource)
		klog.Infof("Node %s available resources: %+v", nodeName, nodeInfo.availableResource)

		for _, parser := range resourceParserMap {
			match := false

			accelInfo, err := parser.ParseFromKubernetes(nodeInfo.allocatableResource, nodeInfo.labels)
			if err != nil {
				return nil, fmt.Errorf("failed to parse allocatable resources from Kubernetes: %w", err)
			}

			if accelInfo != nil {
				for key, group := range accelInfo.AcceleratorGroups {
					if existingGroup, exists := result.Allocatable.AcceleratorGroups[key]; exists {
						existingGroup.Quantity += group.Quantity
						for productKey, quantity := range group.ProductGroups {
							if existingQuantity, exists := existingGroup.ProductGroups[productKey]; exists {
								existingGroup.ProductGroups[productKey] = existingQuantity + quantity
							} else {
								existingGroup.ProductGroups[productKey] = quantity
							}
						}

						result.Allocatable.AcceleratorGroups[key] = existingGroup
					} else {
						result.Allocatable.AcceleratorGroups[key] = accelInfo.AcceleratorGroups[key]
					}
				}
			}

			accelInfo, err = parser.ParseFromKubernetes(nodeInfo.availableResource, nodeInfo.labels)
			if err != nil {
				return nil, fmt.Errorf("failed to parse available resources from Kubernetes: %w", err)
			}

			if accelInfo != nil {
				match = true

				for key, group := range accelInfo.AcceleratorGroups {
					if existingGroup, exists := result.Available.AcceleratorGroups[key]; exists {
						existingGroup.Quantity += group.Quantity
						for productKey, quantity := range group.ProductGroups {
							if existingQuantity, exists := existingGroup.ProductGroups[productKey]; exists {
								existingGroup.ProductGroups[productKey] = existingQuantity + quantity
							} else {
								existingGroup.ProductGroups[productKey] = quantity
							}
						}

						result.Available.AcceleratorGroups[key] = existingGroup
					} else {
						result.Available.AcceleratorGroups[key] = accelInfo.AcceleratorGroups[key]
					}
				}
			}

			if match {
				break
			}
		}
	}

	if result.Available.CPU < 0 {
		result.Available.CPU = 0
	} else {
		result.Available.CPU = roundFloat64ToTwoDecimals(result.Available.CPU)
	}

	if result.Available.Memory < 0 {
		result.Available.Memory = 0
	} else {
		result.Available.Memory = roundFloat64ToTwoDecimals(result.Available.Memory)
	}

	for _, group := range result.Available.AcceleratorGroups {
		if group.Quantity < 0 {
			group.Quantity = 0
		}
	}

	return result, nil
}

func roundFloat64ToTwoDecimals(input float64) float64 {
	return math.Round(input*100) / 100
}
