package resource

import (
	"context"
	"fmt"
	"math"
	"sort"

	corev1 "k8s.io/api/core/v1"
	k8sresource "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	resourceutil "k8s.io/kubectl/pkg/util/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator/resourceparser"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
)

type ResourceClient interface {
	ListNodes(ctx context.Context, opts ListNodesOptions) ([]ResourceNode, error)
	ListEndpointInstances(ctx context.Context, opts ListEndpointInstancesOptions) ([]EndpointInstanceResource, error)
}

type StaticNodeLister interface {
	ListByCluster(ctx context.Context, workspace, clusterName string) ([]v1.StaticNode, error)
}

const BytesPerGiB = 1024 * 1024 * 1024

type ListNodesOptions struct {
	AcceleratorVirtualizationEnabled bool
}

type ListEndpointInstancesOptions struct {
	EndpointName                     string
	Namespace                        string
	SelectorLabels                   map[string]string
	AcceleratorVirtualizationEnabled bool
}

type ResourceNode struct {
	ID                  string
	Status              *v1.NodeResourceStatus
	AcceleratorMetadata map[v1.AcceleratorType]*v1.AcceleratorMetadata
}

type EndpointInstanceResource = resourceparser.EndpointInstanceResource

// Deprecated: use resourceparser.ResourceParser. This alias keeps generated
// accelerator mocks compatible while the canonical interface lives in the
// accelerator resourceparser package.
type ResourceParser = resourceparser.ResourceParser

type K8sResourceClient struct {
	client  client.Client
	parsers map[string]resourceparser.ResourceParser
}

func NewK8sResourceClient(ctrClient client.Client, parsers map[string]resourceparser.ResourceParser) *K8sResourceClient {
	return &K8sResourceClient{
		client:  ctrClient,
		parsers: parsers,
	}
}

func (c *K8sResourceClient) ListNodes(ctx context.Context, opts ListNodesOptions) ([]ResourceNode, error) {
	nodeList := &corev1.NodeList{}
	if err := c.client.List(ctx, nodeList); err != nil {
		return nil, fmt.Errorf("failed to list k8s nodes: %w", err)
	}

	podList := &corev1.PodList{}
	if err := c.client.List(ctx, podList, &client.ListOptions{
		Raw: &metav1.ListOptions{
			FieldSelector: "spec.nodeName!=,status.phase!=Failed,status.phase!=Succeeded",
		},
	}); err != nil {
		return nil, fmt.Errorf("failed to list k8s pods: %w", err)
	}

	type nodeResourceInfo struct {
		allocatableResource map[corev1.ResourceName]k8sresource.Quantity
		availableResource   map[corev1.ResourceName]k8sresource.Quantity
		labels              map[string]string
		annotations         map[string]string
		pods                []resourceparser.KubernetesPodResourceContext
	}
	nodesByID := make(map[string]*nodeResourceInfo)

	for _, node := range nodeList.Items {
		if node.Spec.Unschedulable {
			continue
		}

		nodesByID[node.Name] = &nodeResourceInfo{
			allocatableResource: node.Status.Allocatable.DeepCopy(),
			availableResource:   node.Status.Allocatable.DeepCopy(),
			labels:              node.Labels,
			annotations:         node.Annotations,
		}
	}

	for _, pod := range podList.Items {
		nodeName := pod.Spec.NodeName
		if nodeName == "" || pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded {
			continue
		}

		nodeInfo, exists := nodesByID[nodeName]
		if !exists {
			continue
		}

		totalRequested, totalLimits := resourceutil.PodRequestsAndLimits(&pod)
		nodeInfo.pods = append(nodeInfo.pods, resourceparser.KubernetesPodResourceContext{
			Namespace:   pod.Namespace,
			Name:        pod.Name,
			UID:         string(pod.UID),
			NodeName:    nodeName,
			Labels:      pod.Labels,
			Requests:    totalRequested,
			Limits:      totalLimits,
			Annotations: pod.Annotations,
		})

		for resourceName, quantity := range totalRequested {
			if existingQty, exists := nodeInfo.availableResource[resourceName]; exists {
				existingQty.Sub(quantity)
				nodeInfo.availableResource[resourceName] = existingQty
			} else {
				// HAMi-only Pod resources such as nvidia.com/gpucores are not
				// exposed on Node allocatable. They are accounted by the
				// virtualization parser using HAMi annotations instead.
				klog.V(4).Infof("pod %s requests resource %s that is not exposed in node %s allocatable resources",
					pod.Name, resourceName, nodeName)
			}
		}
	}

	nodes := make([]ResourceNode, 0, len(nodesByID))

	for nodeID, nodeInfo := range nodesByID {
		klog.V(4).Infof("Node %s allocatable resources: %+v", nodeID, nodeInfo.allocatableResource)
		klog.V(4).Infof("Node %s available resources: %+v", nodeID, nodeInfo.availableResource)

		resourceStatus, devices, metadata, err := transformKubernetesNodeResources(
			resourceparser.KubernetesNodeResourceContext{
				NodeName:             nodeID,
				AllocatableResources: nodeInfo.allocatableResource,
				AvailableResources:   nodeInfo.availableResource,
				Labels:               nodeInfo.labels,
				Annotations:          nodeInfo.annotations,
				Pods:                 nodeInfo.pods,
			},
			c.parsers,
			opts.AcceleratorVirtualizationEnabled,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to transform resources for node %s: %w", nodeID, err)
		}

		node := resourceNodeFromStatus(nodeID, resourceStatus)
		if node.Status != nil && len(devices) > 0 {
			node.Status.Devices = devices
		}

		mergeAcceleratorMetadata(node.AcceleratorMetadata, metadata)

		nodes = append(nodes, node)
	}

	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].ID < nodes[j].ID
	})

	return nodes, nil
}

func (c *K8sResourceClient) ListEndpointInstances(
	ctx context.Context,
	opts ListEndpointInstancesOptions,
) ([]EndpointInstanceResource, error) {
	if opts.Namespace == "" {
		return nil, fmt.Errorf("endpoint namespace is empty")
	}

	selectorLabels := opts.SelectorLabels
	if len(selectorLabels) == 0 && opts.EndpointName != "" {
		selectorLabels = map[string]string{
			"app":      "inference",
			"endpoint": opts.EndpointName,
		}
	}

	if len(selectorLabels) == 0 {
		return nil, fmt.Errorf("endpoint selector labels are empty")
	}

	if !opts.AcceleratorVirtualizationEnabled {
		return nil, nil
	}

	podList := &corev1.PodList{}
	if err := c.client.List(ctx, podList,
		client.InNamespace(opts.Namespace),
		client.MatchingLabels(selectorLabels),
	); err != nil {
		return nil, fmt.Errorf("failed to list endpoint pods: %w", err)
	}

	pods := make([]resourceparser.KubernetesPodResourceContext, 0, len(podList.Items))
	nodeNames := make(map[string]struct{})

	for _, pod := range podList.Items {
		if pod.Spec.NodeName == "" || pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded {
			continue
		}

		totalRequested, totalLimits := resourceutil.PodRequestsAndLimits(&pod)
		pods = append(pods, resourceparser.KubernetesPodResourceContext{
			Namespace:   pod.Namespace,
			Name:        pod.Name,
			UID:         string(pod.UID),
			NodeName:    pod.Spec.NodeName,
			Labels:      pod.Labels,
			Requests:    totalRequested,
			Limits:      totalLimits,
			Annotations: pod.Annotations,
		})
		nodeNames[pod.Spec.NodeName] = struct{}{}
	}

	nodes, err := c.listEndpointNodes(ctx, nodeNames)
	if err != nil {
		return nil, err
	}

	input := resourceparser.KubernetesEndpointResourceContext{
		EndpointName: opts.EndpointName,
		Namespace:    opts.Namespace,
		Pods:         pods,
		Nodes:        nodes,
	}
	if instances, ok, err := transformKubernetesVirtualizationEndpointResources(input, c.parsers); ok || err != nil {
		if err != nil {
			return nil, err
		}

		sortEndpointInstanceResources(instances)

		return instances, nil
	}

	return nil, nil
}

func (c *K8sResourceClient) listEndpointNodes(
	ctx context.Context,
	nodeNames map[string]struct{},
) (map[string]resourceparser.KubernetesEndpointNodeResourceContext, error) {
	nodes := make(map[string]resourceparser.KubernetesEndpointNodeResourceContext)
	if len(nodeNames) == 0 {
		return nodes, nil
	}

	nodeList := &corev1.NodeList{}
	if err := c.client.List(ctx, nodeList); err != nil {
		return nil, fmt.Errorf("failed to list endpoint nodes: %w", err)
	}

	for _, node := range nodeList.Items {
		if _, ok := nodeNames[node.Name]; !ok {
			continue
		}

		nodes[node.Name] = resourceparser.KubernetesEndpointNodeResourceContext{
			Name:        node.Name,
			Labels:      node.Labels,
			Annotations: node.Annotations,
		}
	}

	return nodes, nil
}

func sortedParserKeys(parsers map[string]resourceparser.ResourceParser) []string {
	keys := make([]string, 0, len(parsers))
	for key := range parsers {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	return keys
}

type RayResourceClient struct {
	client  dashboard.DashboardService
	parsers map[string]resourceparser.ResourceParser
}

func NewRayResourceClient(dashboardClient dashboard.DashboardService, parsers map[string]resourceparser.ResourceParser) *RayResourceClient {
	return &RayResourceClient{
		client:  dashboardClient,
		parsers: parsers,
	}
}

func (c *RayResourceClient) ListNodes(_ context.Context, _ ListNodesOptions) ([]ResourceNode, error) {
	nodeList, err := c.client.ListNodes()
	if err != nil {
		return nil, fmt.Errorf("failed to get node list from Ray Dashboard: %w", err)
	}

	nodes := make([]ResourceNode, 0, len(nodeList))

	for _, node := range nodeList {
		if node.Raylet.State != v1.AliveNodeState {
			continue
		}

		availableResources := map[string]float64{}
		allocatableResources := map[string]float64{}

		for resourceKey, quantity := range node.Raylet.Resources {
			allocatableResources[resourceKey] = quantity
			availableResources[resourceKey] = quantity
		}

		for _, workers := range node.Raylet.CoreWorkersStats {
			for resourceKey, allocations := range workers.UsedResources {
				availableResources[resourceKey] -= float64(allocations.TotalAllocation())
			}
		}

		klog.V(4).Infof("Node %s allocatable resources: %+v", node.IP, allocatableResources)
		klog.V(4).Infof("Node %s available resources: %+v", node.IP, availableResources)

		resourceStatus, err := transformRayResources(availableResources, allocatableResources, c.parsers)
		if err != nil {
			return nil, fmt.Errorf("failed to transform resources for node %s: %w", node.IP, err)
		}

		nodes = append(nodes, resourceNodeFromStatus(node.IP, resourceStatus))
	}

	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].ID < nodes[j].ID
	})

	return nodes, nil
}

func (c *RayResourceClient) ListEndpointInstances(
	_ context.Context,
	_ ListEndpointInstancesOptions,
) ([]EndpointInstanceResource, error) {
	return nil, nil
}

func resourceNodeFromStatus(nodeID string, status *v1.ResourceStatus) ResourceNode {
	metadata := detachAcceleratorMetadata(status)

	return ResourceNode{
		ID: nodeID,
		Status: &v1.NodeResourceStatus{
			ResourceStatus: *status,
		},
		AcceleratorMetadata: metadata,
	}
}

func transformKubernetesNodeResources(
	input resourceparser.KubernetesNodeResourceContext,
	parsers map[string]resourceparser.ResourceParser,
	acceleratorVirtualizationEnabled bool,
) (*v1.ResourceStatus, []*v1.DeviceResource, map[v1.AcceleratorType]*v1.AcceleratorMetadata, error) {
	status := newKubernetesResourceStatus(input.AvailableResources, input.AllocatableResources)

	if acceleratorVirtualizationEnabled {
		// CPU and memory always come from Kubernetes native allocatable/requests.
		// Accelerator semantics switch only when a virtualization parser matches
		// the node; otherwise standard GPU parsing still applies.
		if result, ok, err := transformKubernetesVirtualizationNodeResources(input, parsers); ok || err != nil {
			if err != nil {
				return nil, nil, nil, err
			}

			if result == nil {
				return nil, nil, nil, fmt.Errorf("kubernetes virtualization resource parser returned nil result")
			}

			mergeKubernetesVirtualizationAccelerators(status, result)

			return status, result.Devices, result.AcceleratorMetadata, nil
		}
	}

	if err := mergeKubernetesStandardAccelerators(status, input.AvailableResources, input.AllocatableResources,
		input.Labels, parsers); err != nil {
		return nil, nil, nil, err
	}

	return status, nil, nil, nil
}

func transformKubernetesVirtualizationNodeResources(
	input resourceparser.KubernetesNodeResourceContext,
	parsers map[string]resourceparser.ResourceParser,
) (*resourceparser.KubernetesResourceParseResult, bool, error) {
	for _, key := range sortedParserKeys(parsers) {
		parser, ok := parsers[key].(resourceparser.KubernetesVirtualizationResourceParser)
		if !ok {
			continue
		}

		result, matched, err := parser.ParseKubernetesVirtualizationNode(input)
		if err != nil {
			return nil, true, fmt.Errorf("failed to parse Kubernetes virtualization resources for parser %s: %w", key, err)
		}

		if matched {
			// Use only one virtualization parser per node. Mixing parser results
			// would blend incompatible accelerator resource semantics.
			return result, true, nil
		}
	}

	return nil, false, nil
}

func mergeKubernetesVirtualizationAccelerators(
	status *v1.ResourceStatus,
	result *resourceparser.KubernetesResourceParseResult,
) {
	if status == nil || result == nil {
		return
	}

	mergeKubernetesResourceInfoAccelerators(status.Allocatable, result.Allocatable)
	mergeKubernetesResourceInfoAccelerators(status.Available, result.Available)
}

func mergeKubernetesStandardAccelerators(
	result *v1.ResourceStatus,
	availableResources, allocatableResources map[corev1.ResourceName]k8sresource.Quantity,
	labels map[string]string,
	parsers map[string]resourceparser.ResourceParser,
) error {
	for _, key := range sortedParserKeys(parsers) {
		parser := parsers[key]
		allocatableInfo, err := parser.ParseFromKubernetes(allocatableResources, labels)

		if err != nil {
			return fmt.Errorf("failed to parse allocatable Kubernetes resources for parser %s: %w", key, err)
		}

		if allocatableInfo != nil && len(allocatableInfo.AcceleratorGroups) > 0 {
			mergeAcceleratorGroups(result.Allocatable.AcceleratorGroups, allocatableInfo.AcceleratorGroups)
			mergeResourceInfoMetadata(result.Allocatable, allocatableInfo.AcceleratorMetadata)
		}

		availableInfo, err := parser.ParseFromKubernetes(availableResources, labels)
		if err != nil {
			return fmt.Errorf("failed to parse available Kubernetes resources for parser %s: %w", key, err)
		}

		if availableInfo != nil && len(availableInfo.AcceleratorGroups) > 0 {
			mergeAcceleratorGroups(result.Available.AcceleratorGroups, availableInfo.AcceleratorGroups)
			mergeResourceInfoMetadata(result.Available, availableInfo.AcceleratorMetadata)
		}
	}

	return nil
}

func transformKubernetesVirtualizationEndpointResources(
	input resourceparser.KubernetesEndpointResourceContext,
	parsers map[string]resourceparser.ResourceParser,
) ([]EndpointInstanceResource, bool, error) {
	for _, key := range sortedParserKeys(parsers) {
		parser, ok := parsers[key].(resourceparser.KubernetesVirtualizationEndpointResourceParser)
		if !ok {
			continue
		}

		instances, matched, err := parser.ParseKubernetesVirtualizationEndpoint(input)
		if err != nil {
			return nil, true, fmt.Errorf("failed to parse Kubernetes virtualization endpoint resources for parser %s: %w", key, err)
		}

		if !matched {
			continue
		}

		// Endpoint resource details are backend-specific. Stop after the first
		// match so resource semantics come from one parser only.
		return instances, true, nil
	}

	return nil, false, nil
}

func newKubernetesResourceStatus(
	availableResources, allocatableResources map[corev1.ResourceName]k8sresource.Quantity,
) *v1.ResourceStatus {
	return &v1.ResourceStatus{
		Allocatable: &v1.ResourceInfo{
			CPU:               quantityCPU(allocatableResources[corev1.ResourceCPU]),
			Memory:            quantityMemoryGiB(allocatableResources[corev1.ResourceMemory]),
			AcceleratorGroups: make(map[v1.AcceleratorType]*v1.AcceleratorGroup),
		},
		Available: &v1.ResourceInfo{
			CPU:               quantityCPU(availableResources[corev1.ResourceCPU]),
			Memory:            quantityMemoryGiB(availableResources[corev1.ResourceMemory]),
			AcceleratorGroups: make(map[v1.AcceleratorType]*v1.AcceleratorGroup),
		},
	}
}

func transformRayResources(
	availableResources, allocatableResources map[string]float64,
	parsers map[string]resourceparser.ResourceParser,
) (*v1.ResourceStatus, error) {
	result := &v1.ResourceStatus{
		Allocatable: &v1.ResourceInfo{
			CPU:               nonNegativeRounded(allocatableResources["CPU"]),
			Memory:            memoryBytesToGiB(allocatableResources["memory"]),
			AcceleratorGroups: make(map[v1.AcceleratorType]*v1.AcceleratorGroup),
		},
		Available: &v1.ResourceInfo{
			CPU:               nonNegativeRounded(availableResources["CPU"]),
			Memory:            memoryBytesToGiB(availableResources["memory"]),
			AcceleratorGroups: make(map[v1.AcceleratorType]*v1.AcceleratorGroup),
		},
	}

	for resourceKey, parser := range parsers {
		allocatableInfo, err := parser.ParseFromRay(allocatableResources)
		if err != nil {
			return nil, fmt.Errorf("failed to parse allocatable resources from Ray for resource %s: %w", resourceKey, err)
		}

		if allocatableInfo != nil && len(allocatableInfo.AcceleratorGroups) > 0 {
			mergeAcceleratorGroups(result.Allocatable.AcceleratorGroups, allocatableInfo.AcceleratorGroups)
			mergeResourceInfoMetadata(result.Allocatable, allocatableInfo.AcceleratorMetadata)
		}

		availableInfo, err := parser.ParseFromRay(availableResources)
		if err != nil {
			return nil, fmt.Errorf("failed to parse available resources from Ray for resource %s: %w", resourceKey, err)
		}

		if availableInfo != nil && len(availableInfo.AcceleratorGroups) > 0 {
			mergeAcceleratorGroups(result.Available.AcceleratorGroups, availableInfo.AcceleratorGroups)
			mergeResourceInfoMetadata(result.Available, availableInfo.AcceleratorMetadata)
		}
	}

	return result, nil
}

func quantityCPU(quantity k8sresource.Quantity) float64 {
	return nonNegativeRounded(quantity.AsApproximateFloat64())
}

func quantityMemoryGiB(quantity k8sresource.Quantity) float64 {
	return memoryBytesToGiB(quantity.AsApproximateFloat64())
}

func memoryBytesToGiB(value float64) float64 {
	if value < 0 {
		return 0
	}

	return roundFloat64ToTwoDecimals(value / BytesPerGiB)
}

func nonNegativeRounded(value float64) float64 {
	if value < 0 {
		return 0
	}

	return roundFloat64ToTwoDecimals(value)
}

func roundFloat64ToTwoDecimals(input float64) float64 {
	return math.Round(input*100) / 100
}

func sortEndpointInstanceResources(instances []EndpointInstanceResource) {
	sort.Slice(instances, func(i, j int) bool {
		if instances[i].NodeID != instances[j].NodeID {
			return instances[i].NodeID < instances[j].NodeID
		}

		if instances[i].InstanceID != instances[j].InstanceID {
			return instances[i].InstanceID < instances[j].InstanceID
		}

		return instances[i].ReplicaID < instances[j].ReplicaID
	})

	for i := range instances {
		sort.Slice(instances[i].Devices, func(j, k int) bool {
			if instances[i].Devices[j].NodeID != instances[i].Devices[k].NodeID {
				return instances[i].Devices[j].NodeID < instances[i].Devices[k].NodeID
			}

			return instances[i].Devices[j].UUID < instances[i].Devices[k].UUID
		})
	}
}

func mergeResourceInfoMetadata(
	info *v1.ResourceInfo,
	metadata map[v1.AcceleratorType]*v1.AcceleratorMetadata,
) {
	if info == nil || len(metadata) == 0 {
		return
	}

	if info.AcceleratorMetadata == nil {
		info.AcceleratorMetadata = make(map[v1.AcceleratorType]*v1.AcceleratorMetadata)
	}

	mergeAcceleratorMetadata(info.AcceleratorMetadata, metadata)
}

func mergeKubernetesResourceInfoAccelerators(target, source *v1.ResourceInfo) {
	if target == nil || source == nil {
		return
	}

	mergeAcceleratorGroups(target.AcceleratorGroups, source.AcceleratorGroups)
	mergeResourceInfoMetadata(target, source.AcceleratorMetadata)
}

func detachAcceleratorMetadata(status *v1.ResourceStatus) map[v1.AcceleratorType]*v1.AcceleratorMetadata {
	metadata := make(map[v1.AcceleratorType]*v1.AcceleratorMetadata)
	if status == nil {
		return metadata
	}
	// Product metadata describes the accelerator type, not node capacity. Keep
	// node resources focused on Used/Total and aggregate metadata at cluster
	// level to avoid repeating it under every node.
	if status.Allocatable != nil {
		mergeAcceleratorMetadata(metadata, status.Allocatable.AcceleratorMetadata)
		status.Allocatable.AcceleratorMetadata = nil
	}

	if status.Available != nil {
		mergeAcceleratorMetadata(metadata, status.Available.AcceleratorMetadata)
		status.Available.AcceleratorMetadata = nil
	}

	return metadata
}

func mergeAcceleratorGroups(target, source map[v1.AcceleratorType]*v1.AcceleratorGroup) {
	for acceleratorType, sourceGroup := range source {
		if sourceGroup == nil {
			continue
		}

		targetGroup := target[acceleratorType]
		if targetGroup == nil {
			targetGroup = &v1.AcceleratorGroup{}
			target[acceleratorType] = targetGroup
		}

		targetGroup.Quantity += sourceGroup.Quantity

		if len(sourceGroup.ProductGroups) > 0 {
			if targetGroup.ProductGroups == nil {
				targetGroup.ProductGroups = make(map[v1.AcceleratorProduct]float64)
			}

			for productType, quantity := range sourceGroup.ProductGroups {
				targetGroup.ProductGroups[productType] += quantity
			}
		}

		if len(sourceGroup.Products) > 0 {
			if targetGroup.Products == nil {
				targetGroup.Products = make(map[v1.AcceleratorProduct]*v1.AcceleratorProductResource)
			}

			for productType, sourceProduct := range sourceGroup.Products {
				if sourceProduct == nil {
					continue
				}

				targetProduct := targetGroup.Products[productType]
				if targetProduct == nil {
					targetProduct = &v1.AcceleratorProductResource{}
					targetGroup.Products[productType] = targetProduct
				}

				targetProduct.Quantity += sourceProduct.Quantity

				if sourceProduct.Virtualization != nil {
					if targetProduct.Virtualization == nil {
						targetProduct.Virtualization = &v1.AcceleratorVirtualizationResource{}
					}

					targetProduct.Virtualization.MemoryMiB += sourceProduct.Virtualization.MemoryMiB
					targetProduct.Virtualization.CoreUnits += sourceProduct.Virtualization.CoreUnits
				}
			}
		}
	}
}

func mergeAcceleratorMetadata(target, source map[v1.AcceleratorType]*v1.AcceleratorMetadata) {
	for acceleratorType, sourceMetadata := range source {
		if sourceMetadata == nil || len(sourceMetadata.Products) == 0 {
			continue
		}

		targetMetadata := target[acceleratorType]
		if targetMetadata == nil {
			targetMetadata = &v1.AcceleratorMetadata{
				Products: make(map[v1.AcceleratorProduct]*v1.AcceleratorProductMetadata),
			}
			target[acceleratorType] = targetMetadata
		}

		if targetMetadata.Products == nil {
			targetMetadata.Products = make(map[v1.AcceleratorProduct]*v1.AcceleratorProductMetadata)
		}

		for product, sourceProduct := range sourceMetadata.Products {
			if sourceProduct == nil {
				continue
			}

			if _, exists := targetMetadata.Products[product]; !exists {
				targetMetadata.Products[product] = &v1.AcceleratorProductMetadata{
					MemoryTotalMiB: sourceProduct.MemoryTotalMiB,
				}
			}
		}
	}
}
