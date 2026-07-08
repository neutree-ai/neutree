package resource

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	k8sresource "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	resourceutil "k8s.io/kubectl/pkg/util/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator/resourceparser"
	"github.com/neutree-ai/neutree/internal/util"
)

const kubernetesDefaultDeviceCoreUnits = int64(100)

type kubernetesNeutreeDeviceAnnotation struct {
	ID          string `json:"id,omitempty"`
	UUID        string `json:"uuid,omitempty"`
	MinorNumber *int   `json:"minor_number,omitempty"`
	MemoryMiB   int64  `json:"memory_mib,omitempty"`
	Healthy     bool   `json:"healthy,omitempty"`
}

type kubernetesNeutreeDeviceOrder struct {
	MinorNumber *int
	Order       *int
}

type kubernetesNeutreeDeviceUsage struct {
	memoryMiB int64
	coreUnits int64
}

type kubernetesNeutreeDeviceResource struct {
	uuid        string
	memoryMiB   int64
	coreUnits   int64
	healthy     bool
	minorNumber *int
	order       *int
}

type kubernetesNeutreeAcceleratorResources struct {
	Allocatable         *v1.ResourceInfo
	Available           *v1.ResourceInfo
	Devices             []*v1.DeviceResource
	AcceleratorMetadata map[v1.AcceleratorType]*v1.AcceleratorMetadata
}

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

func (c *K8sResourceClient) ListNodes(ctx context.Context, _ *v1.Cluster) ([]ResourceNode, error) {
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

		podContext := kubernetesPodResourceContext(pod)
		totalRequested := podContext.Requests
		nodeInfo.pods = append(nodeInfo.pods, podContext)

		for resourceName, quantity := range totalRequested {
			if existingQty, exists := nodeInfo.availableResource[resourceName]; exists {
				existingQty.Sub(quantity)
				nodeInfo.availableResource[resourceName] = existingQty
			} else {
				// Slice resources may not be exposed on Node allocatable. Device-level
				// capacity is accounted from Neutree annotations when available.
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
	cluster *v1.Cluster,
	endpoint *v1.Endpoint,
) ([]EndpointInstanceResource, error) {
	namespace := ""
	if cluster != nil {
		namespace = util.ClusterNamespace(cluster)
	}

	if namespace == "" {
		return nil, fmt.Errorf("endpoint namespace is empty")
	}

	selectorLabels := map[string]string{
		"app":      "inference",
		"endpoint": endpoint.Metadata.Name,
	}

	podList := &corev1.PodList{}
	if err := c.client.List(ctx, podList,
		client.InNamespace(namespace),
		client.MatchingLabels(selectorLabels),
	); err != nil {
		return nil, fmt.Errorf("failed to list endpoint pods: %w", err)
	}

	pods := make([]resourceparser.KubernetesPodResourceContext, 0, len(podList.Items))

	for _, pod := range podList.Items {
		if pod.Spec.NodeName == "" || pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded {
			continue
		}

		pods = append(pods, kubernetesPodResourceContext(pod))
	}

	instances := endpointInstancesFromNeutreeAnnotations(resourceparser.KubernetesEndpointResourceContext{
		EndpointName: endpoint.Metadata.Name,
		Namespace:    namespace,
		Pods:         pods,
	})

	if len(instances) == 0 {
		return nil, nil
	}

	sortEndpointInstanceResources(instances)

	return instances, nil
}

func kubernetesPodResourceContext(pod corev1.Pod) resourceparser.KubernetesPodResourceContext {
	totalRequested, totalLimits := resourceutil.PodRequestsAndLimits(&pod)

	return resourceparser.KubernetesPodResourceContext{
		Namespace:   pod.Namespace,
		Name:        pod.Name,
		UID:         string(pod.UID),
		NodeName:    pod.Spec.NodeName,
		Labels:      pod.Labels,
		Requests:    totalRequested,
		Limits:      totalLimits,
		Annotations: pod.Annotations,
	}
}

func transformKubernetesNodeResources(
	input resourceparser.KubernetesNodeResourceContext,
	parsers map[string]resourceparser.ResourceParser,
) (*v1.ResourceStatus, []*v1.DeviceResource, map[v1.AcceleratorType]*v1.AcceleratorMetadata, error) {
	status := newKubernetesResourceStatus(input.AvailableResources, input.AllocatableResources)

	if err := mergeKubernetesStandardAccelerators(status, input.AvailableResources, input.AllocatableResources, input.Labels, parsers); err != nil {
		return nil, nil, nil, err
	}

	result, matched, err := kubernetesNeutreeAcceleratorResourcesFromAnnotations(
		status,
		input.Annotations,
		input.Pods,
	)
	if err != nil {
		klog.Warningf("Skipping Neutree accelerator annotation enhancement for node %s: %v", input.NodeName, err)
		return status, nil, nil, nil
	}

	if matched && result != nil {
		enhanceKubernetesResourceStatusWithNeutreeAnnotations(status, result)
		return status, result.Devices, result.AcceleratorMetadata, nil
	}

	return status, nil, nil, nil
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

func kubernetesNeutreeAcceleratorResourcesFromAnnotations(
	base *v1.ResourceStatus,
	annotations map[string]string,
	pods []resourceparser.KubernetesPodResourceContext,
) (*kubernetesNeutreeAcceleratorResources, bool, error) {
	devices, err := parseKubernetesNeutreeDeviceResources(annotations)
	if err != nil {
		return nil, true, err
	}

	if len(devices) == 0 {
		return nil, false, nil
	}

	acceleratorType, product, ok := kubernetesBaseAcceleratorProduct(base)
	if !ok {
		return nil, true, nil
	}

	usage := kubernetesNeutreeDeviceUsageByUUID(pods)

	return buildKubernetesNeutreeAcceleratorResources(devices, usage, acceleratorType, product), true, nil
}

func endpointInstancesFromNeutreeAnnotations(
	input resourceparser.KubernetesEndpointResourceContext,
) []EndpointInstanceResource {
	instances := make([]EndpointInstanceResource, 0, len(input.Pods))

	for _, pod := range input.Pods {
		devices, err := parseKubernetesNeutreeEndpointAllocations(
			pod.Annotations[resourceparser.NeutreeAcceleratorAllocationsAnnotation],
			pod.NodeName,
		)
		if err != nil {
			klog.Warningf("Skipping malformed Neutree accelerator allocation annotation for pod %s/%s: %v",
				pod.Namespace, pod.Name, err)
			continue
		}

		if len(devices) == 0 {
			continue
		}

		instances = append(instances, EndpointInstanceResource{
			InstanceID: pod.Name,
			ReplicaID:  pod.Name,
			NodeID:     pod.NodeName,
			Devices:    devices,
		})
	}

	return instances
}

func parseKubernetesNeutreeEndpointAllocations(
	value string,
	nodeName string,
) ([]v1.DeviceAllocation, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}

	var allocations []v1.DeviceAllocation
	if err := json.Unmarshal([]byte(value), &allocations); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w",
			resourceparser.NeutreeAcceleratorAllocationsAnnotation, err)
	}

	for i := range allocations {
		if allocations[i].NodeID == "" {
			allocations[i].NodeID = nodeName
		}
	}

	return allocations, nil
}

func parseKubernetesNeutreeDeviceResources(
	annotations map[string]string,
) ([]kubernetesNeutreeDeviceResource, error) {
	value := annotations[resourceparser.NeutreeAcceleratorDevicesAnnotation]
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}

	var devices []kubernetesNeutreeDeviceAnnotation
	if err := json.Unmarshal([]byte(value), &devices); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w",
			resourceparser.NeutreeAcceleratorDevicesAnnotation, err)
	}

	devicesWithMinor := make([]kubernetesNeutreeDeviceAnnotation, 0, len(devices))

	for _, device := range devices {
		uuid := kubernetesNeutreeDeviceUUID(device)
		if uuid == "" || device.MinorNumber == nil {
			continue
		}

		device.UUID = uuid
		devicesWithMinor = append(devicesWithMinor, device)
	}

	sort.SliceStable(devicesWithMinor, func(i, j int) bool {
		return *devicesWithMinor[i].MinorNumber < *devicesWithMinor[j].MinorNumber
	})

	orders := make(map[string]kubernetesNeutreeDeviceOrder, len(devicesWithMinor))

	for order, device := range devicesWithMinor {
		minorNumber := *device.MinorNumber
		displayOrder := order
		orders[device.UUID] = kubernetesNeutreeDeviceOrder{
			MinorNumber: &minorNumber,
			Order:       &displayOrder,
		}
	}

	result := make([]kubernetesNeutreeDeviceResource, 0, len(devices))

	for _, device := range devices {
		uuid := kubernetesNeutreeDeviceUUID(device)
		if uuid == "" {
			continue
		}

		order := orders[uuid]
		result = append(result, kubernetesNeutreeDeviceResource{
			uuid:        uuid,
			memoryMiB:   device.MemoryMiB,
			coreUnits:   kubernetesDefaultDeviceCoreUnits,
			healthy:     device.Healthy,
			minorNumber: order.MinorNumber,
			order:       order.Order,
		})
	}

	return result, nil
}

func kubernetesNeutreeDeviceUsageByUUID(
	pods []resourceparser.KubernetesPodResourceContext,
) map[string]kubernetesNeutreeDeviceUsage {
	result := make(map[string]kubernetesNeutreeDeviceUsage)

	for _, pod := range pods {
		allocations, err := parseKubernetesNeutreeEndpointAllocations(
			pod.Annotations[resourceparser.NeutreeAcceleratorAllocationsAnnotation],
			pod.NodeName,
		)
		if err != nil {
			klog.Warningf("Skipping malformed Neutree accelerator allocation annotation for pod %s/%s: %v",
				pod.Namespace, pod.Name, err)
			continue
		}

		for _, allocation := range allocations {
			usage := result[allocation.UUID]
			usage.memoryMiB += allocation.MemoryMiB
			usage.coreUnits += allocation.CoreUnits
			result[allocation.UUID] = usage
		}
	}

	return result
}

func buildKubernetesNeutreeAcceleratorResources(
	devices []kubernetesNeutreeDeviceResource,
	usage map[string]kubernetesNeutreeDeviceUsage,
	acceleratorType v1.AcceleratorType,
	product v1.AcceleratorProduct,
) *kubernetesNeutreeAcceleratorResources {
	allocatableGroup := newAcceleratorGroup()
	availableGroup := newAcceleratorGroup()
	metadata := &v1.AcceleratorMetadata{
		Products: make(map[v1.AcceleratorProduct]*v1.AcceleratorProductMetadata),
	}
	deviceResources := make([]*v1.DeviceResource, 0, len(devices))

	for _, device := range devices {
		allocatable := &v1.DeviceResourcePool{
			MemoryMiB: nonZeroInt64(device.memoryMiB, 0),
			CoreUnits: nonZeroInt64(device.coreUnits, kubernetesDefaultDeviceCoreUnits),
		}
		used := usage[device.uuid]
		available := &v1.DeviceResourcePool{
			MemoryMiB: nonNegativeInt64(allocatable.MemoryMiB - used.memoryMiB),
			CoreUnits: nonNegativeInt64(allocatable.CoreUnits - used.coreUnits),
		}

		if !device.healthy {
			allocatable = &v1.DeviceResourcePool{}
			available = &v1.DeviceResourcePool{}
		}

		deviceResources = append(deviceResources, &v1.DeviceResource{
			UUID:        device.uuid,
			Product:     string(product),
			Health:      device.healthy,
			MinorNumber: device.minorNumber,
			Order:       device.order,
			Allocatable: allocatable,
			Available:   available,
		})

		if !device.healthy {
			continue
		}

		addKubernetesNeutreeProductResource(allocatableGroup, product, 1, allocatable)

		if hasDeviceAvailableCapacity(available) {
			addKubernetesNeutreeProductResource(availableGroup, product, 1, available)
		}

		if _, exists := metadata.Products[product]; !exists && allocatable.MemoryMiB > 0 {
			metadata.Products[product] = &v1.AcceleratorProductMetadata{
				MemoryTotalMiB: float64(allocatable.MemoryMiB),
			}
		}
	}

	return &kubernetesNeutreeAcceleratorResources{
		Allocatable: &v1.ResourceInfo{
			AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
				acceleratorType: allocatableGroup,
			},
		},
		Available: &v1.ResourceInfo{
			AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
				acceleratorType: availableGroup,
			},
		},
		Devices: deviceResources,
		AcceleratorMetadata: map[v1.AcceleratorType]*v1.AcceleratorMetadata{
			acceleratorType: metadata,
		},
	}
}

func enhanceKubernetesResourceStatusWithNeutreeAnnotations(
	status *v1.ResourceStatus,
	result *kubernetesNeutreeAcceleratorResources,
) {
	if status == nil || result == nil {
		return
	}

	replaceKubernetesNeutreeResourceInfo(status.Allocatable, result.Allocatable)
	replaceKubernetesNeutreeResourceInfo(status.Available, result.Available)
}

func kubernetesNeutreeDeviceUUID(device kubernetesNeutreeDeviceAnnotation) string {
	if uuid := strings.TrimSpace(device.UUID); uuid != "" {
		return uuid
	}

	return strings.TrimSpace(device.ID)
}

func addKubernetesNeutreeProductResource(
	group *v1.AcceleratorGroup,
	product v1.AcceleratorProduct,
	quantity float64,
	pool *v1.DeviceResourcePool,
) {
	group.Quantity += quantity
	group.ProductGroups[product] += quantity

	productResource := group.Products[product]
	if productResource == nil {
		productResource = &v1.AcceleratorProductResource{}
		group.Products[product] = productResource
	}

	productResource.Quantity += quantity

	if productResource.Virtualization == nil {
		productResource.Virtualization = &v1.AcceleratorVirtualizationResource{}
	}

	productResource.Virtualization.MemoryMiB += float64(pool.MemoryMiB)
	productResource.Virtualization.CoreUnits += float64(pool.CoreUnits)
}

func replaceKubernetesNeutreeResourceInfo(target, enhancement *v1.ResourceInfo) {
	if target == nil || enhancement == nil {
		return
	}

	// Kubernetes virtualization can scale native device resources such as
	// nvidia.com/gpu beyond the physical card count. Neutree annotations carry
	// the device-level view, so use them to replace group quantities precisely.
	for acceleratorType, sourceGroup := range enhancement.AcceleratorGroups {
		if sourceGroup == nil {
			continue
		}

		targetGroup := target.AcceleratorGroups[acceleratorType]
		if targetGroup == nil {
			continue
		}

		targetGroup.Quantity = sourceGroup.Quantity

		if targetGroup.ProductGroups == nil {
			targetGroup.ProductGroups = make(map[v1.AcceleratorProduct]float64)
		}

		if targetGroup.Products == nil {
			targetGroup.Products = make(map[v1.AcceleratorProduct]*v1.AcceleratorProductResource)
		}

		for _, product := range kubernetesAcceleratorGroupProducts(targetGroup, sourceGroup) {
			sourceQuantity := sourceGroup.ProductGroups[product]
			targetGroup.ProductGroups[product] = sourceQuantity

			targetProduct := targetGroup.Products[product]
			if targetProduct == nil {
				targetProduct = &v1.AcceleratorProductResource{}
				targetGroup.Products[product] = targetProduct
			}

			targetProduct.Quantity = sourceQuantity

			sourceProduct := sourceGroup.Products[product]
			if sourceProduct == nil || sourceProduct.Virtualization == nil {
				targetProduct.Virtualization = nil
				continue
			}

			targetProduct.Virtualization = &v1.AcceleratorVirtualizationResource{
				MemoryMiB: sourceProduct.Virtualization.MemoryMiB,
				CoreUnits: sourceProduct.Virtualization.CoreUnits,
			}
		}
	}
}

func kubernetesAcceleratorGroupProducts(groups ...*v1.AcceleratorGroup) []v1.AcceleratorProduct {
	productSet := make(map[v1.AcceleratorProduct]struct{})

	for _, group := range groups {
		if group == nil {
			continue
		}

		for product := range group.ProductGroups {
			if product != "" {
				productSet[product] = struct{}{}
			}
		}

		for product := range group.Products {
			if product != "" {
				productSet[product] = struct{}{}
			}
		}
	}

	products := make([]v1.AcceleratorProduct, 0, len(productSet))
	for product := range productSet {
		products = append(products, product)
	}

	sort.Slice(products, func(i, j int) bool {
		return products[i] < products[j]
	})

	return products
}

func kubernetesBaseAcceleratorProduct(status *v1.ResourceStatus) (v1.AcceleratorType, v1.AcceleratorProduct, bool) {
	if status == nil {
		return "", "", false
	}

	products := map[v1.AcceleratorType]map[v1.AcceleratorProduct]struct{}{}

	for _, info := range []*v1.ResourceInfo{status.Allocatable, status.Available} {
		if info == nil {
			continue
		}

		for acceleratorType, group := range info.AcceleratorGroups {
			if group == nil {
				continue
			}

			if products[acceleratorType] == nil {
				products[acceleratorType] = map[v1.AcceleratorProduct]struct{}{}
			}

			for product := range group.ProductGroups {
				if product != "" {
					products[acceleratorType][product] = struct{}{}
				}
			}

			for product := range group.Products {
				if product != "" {
					products[acceleratorType][product] = struct{}{}
				}
			}
		}
	}

	if len(products) != 1 {
		return "", "", false
	}

	for acceleratorType, acceleratorProducts := range products {
		if len(acceleratorProducts) != 1 {
			return "", "", false
		}

		for product := range acceleratorProducts {
			return acceleratorType, product, true
		}
	}

	return "", "", false
}

func newAcceleratorGroup() *v1.AcceleratorGroup {
	return &v1.AcceleratorGroup{
		ProductGroups: make(map[v1.AcceleratorProduct]float64),
		Products:      make(map[v1.AcceleratorProduct]*v1.AcceleratorProductResource),
	}
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

func quantityCPU(quantity k8sresource.Quantity) float64 {
	return nonNegativeRounded(quantity.AsApproximateFloat64())
}

func quantityMemoryGiB(quantity k8sresource.Quantity) float64 {
	return memoryBytesToGiB(quantity.AsApproximateFloat64())
}

func sortedParserKeys(parsers map[string]resourceparser.ResourceParser) []string {
	keys := make([]string, 0, len(parsers))
	for key := range parsers {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	return keys
}
