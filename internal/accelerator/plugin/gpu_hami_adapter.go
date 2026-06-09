package plugin

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	v1 "github.com/neutree-ai/neutree/api/v1"
	resourceview "github.com/neutree-ai/neutree/internal/resource"
)

const (
	HAMiNodeNvidiaRegisterAnnotation     = "hami.io/node-nvidia-register"
	HAMiVGPUDevicesAllocatedAnnotation   = "hami.io/vgpu-devices-allocated"
	hamiDefaultNvidiaDeviceCoreUnits     = int64(100)
	hamiNvidiaDeviceAllocationFieldCount = 4
)

type GPUHAMiResourceAdapter struct{}

type GPUVirtualizationResourceAdapter struct {
	hami     GPUHAMiResourceAdapter
	standard GPUStandardResourceAdapter
}

type hamiNvidiaRegisteredDevice struct {
	ID      string `json:"id"`
	DevMem  int64  `json:"devmem"`
	DevCore int64  `json:"devcore"`
	Type    string `json:"type"`
	Health  *bool  `json:"health"`
}

type hamiNvidiaDeviceAllocation struct {
	DeviceID  string
	Product   string
	MemoryMiB int64
	CoreUnits int64
}

type hamiNvidiaDeviceUsage struct {
	memoryMiB int64
	coreUnits int64
}

func (p *GPUResourceParser) KubernetesResourceAdapters(
	ctx resourceview.KubernetesResourceAdapterContext,
) []resourceview.KubernetesResourceAdapter {
	if ctx.AcceleratorVirtualizationEnabled {
		return []resourceview.KubernetesResourceAdapter{
			&GPUVirtualizationResourceAdapter{},
		}
	}

	return []resourceview.KubernetesResourceAdapter{
		&GPUStandardResourceAdapter{},
	}
}

func (p *GPUResourceParser) KubernetesEndpointResourceAdapters(
	ctx resourceview.KubernetesResourceAdapterContext,
) []resourceview.KubernetesEndpointResourceAdapter {
	if !ctx.AcceleratorVirtualizationEnabled {
		return nil
	}

	return []resourceview.KubernetesEndpointResourceAdapter{
		&GPUHAMiResourceAdapter{},
	}
}

func (a *GPUVirtualizationResourceAdapter) MatchKubernetesNode(input resourceview.KubernetesNodeResourceContext) bool {
	return a.hami.MatchKubernetesNode(input) || a.standard.MatchKubernetesNode(input)
}

func (a *GPUVirtualizationResourceAdapter) ParseKubernetesNode(
	input resourceview.KubernetesNodeResourceContext,
) (*resourceview.KubernetesResourceAdapterResult, error) {
	if a.hami.MatchKubernetesNode(input) {
		return a.hami.ParseKubernetesNode(input)
	}
	if a.standard.MatchKubernetesNode(input) {
		return a.standard.ParseKubernetesNode(input)
	}

	return nil, fmt.Errorf("NVIDIA virtualization resource adapter does not match node %s", input.NodeName)
}

func (a *GPUHAMiResourceAdapter) MatchKubernetesNode(input resourceview.KubernetesNodeResourceContext) bool {
	if input.Labels[NvidiaGPUVirtualizationLabelKey] != "true" {
		return false
	}

	devices, err := parseHAMiNvidiaRegisteredDevices(input.Annotations[HAMiNodeNvidiaRegisterAnnotation])
	return err == nil && len(devices) > 0
}

func (a *GPUHAMiResourceAdapter) ParseKubernetesNode(
	input resourceview.KubernetesNodeResourceContext,
) (*resourceview.KubernetesResourceAdapterResult, error) {
	devices, err := parseHAMiNvidiaRegisteredDevices(input.Annotations[HAMiNodeNvidiaRegisterAnnotation])
	if err != nil {
		return nil, err
	}
	if len(devices) == 0 {
		return nil, fmt.Errorf("HAMi NVIDIA node %s has no registered devices", input.NodeName)
	}

	usage, err := parseHAMiNvidiaPodUsage(input.Pods)
	if err != nil {
		return nil, err
	}

	return buildHAMiNvidiaResourceAdapterResult(input.Labels, devices, usage), nil
}

func (a *GPUHAMiResourceAdapter) MatchKubernetesEndpoint(input resourceview.KubernetesEndpointResourceContext) bool {
	for _, pod := range input.Pods {
		if strings.TrimSpace(pod.Annotations[HAMiVGPUDevicesAllocatedAnnotation]) == "" {
			continue
		}

		node := input.Nodes[pod.NodeName]
		if node.Labels[NvidiaGPUVirtualizationLabelKey] == "true" {
			return true
		}
	}

	return false
}

func (a *GPUHAMiResourceAdapter) ParseKubernetesEndpoint(
	input resourceview.KubernetesEndpointResourceContext,
) ([]resourceview.EndpointInstanceResource, error) {
	deviceProducts, err := hamiNvidiaDeviceProductsByNode(input.Nodes)
	if err != nil {
		return nil, err
	}

	instances := make([]resourceview.EndpointInstanceResource, 0, len(input.Pods))
	for _, pod := range input.Pods {
		allocations, err := parseHAMiNvidiaDeviceAllocations(pod.Annotations[HAMiVGPUDevicesAllocatedAnnotation])
		if err != nil {
			return nil, fmt.Errorf("failed to parse HAMi NVIDIA allocation for endpoint pod %s/%s: %w",
				pod.Namespace, pod.Name, err)
		}
		if len(allocations) == 0 {
			continue
		}

		instance := resourceview.EndpointInstanceResource{
			InstanceID: pod.Name,
			ReplicaID:  pod.Name,
			NodeID:     pod.NodeName,
			Devices:    make([]v1.DeviceAllocation, 0, len(allocations)),
		}
		for _, allocation := range allocations {
			product := deviceProducts[pod.NodeName][allocation.DeviceID]
			if product == "" {
				product = allocation.Product
			}
			if product == "" {
				product = "unknown"
			}

			instance.Devices = append(instance.Devices, v1.DeviceAllocation{
				UUID:      allocation.DeviceID,
				Product:   product,
				MemoryMiB: allocation.MemoryMiB,
				CoreUnits: allocation.CoreUnits,
				NodeID:    pod.NodeName,
			})
		}

		instances = append(instances, instance)
	}

	return instances, nil
}

func hamiNvidiaDeviceProductsByNode(
	nodes map[string]resourceview.KubernetesEndpointNodeResourceContext,
) (map[string]map[string]string, error) {
	result := make(map[string]map[string]string)

	for nodeName, node := range nodes {
		devices, err := parseHAMiNvidiaRegisteredDevices(node.Annotations[HAMiNodeNvidiaRegisterAnnotation])
		if err != nil {
			return nil, fmt.Errorf("failed to parse HAMi NVIDIA registered devices for node %s: %w", nodeName, err)
		}
		if len(devices) == 0 {
			continue
		}

		result[nodeName] = make(map[string]string, len(devices))
		for _, device := range devices {
			result[nodeName][device.ID] = hamiNvidiaDeviceProduct(node.Labels, device)
		}
	}

	return result, nil
}

func parseHAMiNvidiaRegisteredDevices(value string) ([]hamiNvidiaRegisteredDevice, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}

	var devices []hamiNvidiaRegisteredDevice
	if err := json.Unmarshal([]byte(value), &devices); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", HAMiNodeNvidiaRegisterAnnotation, err)
	}

	return devices, nil
}

func parseHAMiNvidiaPodUsage(pods []resourceview.KubernetesPodResourceContext) (map[string]hamiNvidiaDeviceUsage, error) {
	result := make(map[string]hamiNvidiaDeviceUsage)

	for _, pod := range pods {
		allocations, err := parseHAMiNvidiaDeviceAllocations(pod.Annotations[HAMiVGPUDevicesAllocatedAnnotation])
		if err != nil {
			return nil, fmt.Errorf("failed to parse HAMi NVIDIA allocation for pod %s/%s: %w",
				pod.Namespace, pod.Name, err)
		}

		for _, allocation := range allocations {
			usage := result[allocation.DeviceID]
			usage.memoryMiB += allocation.MemoryMiB
			usage.coreUnits += allocation.CoreUnits
			result[allocation.DeviceID] = usage
		}
	}

	return result, nil
}

func parseHAMiNvidiaDeviceAllocations(value string) ([]hamiNvidiaDeviceAllocation, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}

	var allocations []hamiNvidiaDeviceAllocation
	for _, entry := range strings.Split(value, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}

		if colon := strings.Index(entry, ":"); colon >= 0 {
			entry = entry[:colon]
		}
		fields := strings.Split(entry, ",")
		if len(fields) < hamiNvidiaDeviceAllocationFieldCount {
			continue
		}

		memoryMiB, err := strconv.ParseInt(strings.TrimSpace(fields[2]), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid memory value %q: %w", fields[2], err)
		}
		coreUnits, err := strconv.ParseInt(strings.TrimSpace(fields[3]), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid core value %q: %w", fields[3], err)
		}

		allocations = append(allocations, hamiNvidiaDeviceAllocation{
			DeviceID:  strings.TrimSpace(fields[0]),
			Product:   strings.TrimSpace(fields[1]),
			MemoryMiB: memoryMiB,
			CoreUnits: coreUnits,
		})
	}

	return allocations, nil
}

func buildHAMiNvidiaResourceAdapterResult(
	labels map[string]string,
	devices []hamiNvidiaRegisteredDevice,
	usage map[string]hamiNvidiaDeviceUsage,
) *resourceview.KubernetesResourceAdapterResult {
	allocatableGroup := newNvidiaAcceleratorGroup()
	availableGroup := newNvidiaAcceleratorGroup()
	metadata := &v1.AcceleratorMetadata{
		Products: make(map[v1.AcceleratorProduct]*v1.AcceleratorProductMetadata),
	}
	deviceResources := make([]*v1.DeviceResource, 0, len(devices))

	for _, device := range devices {
		product := hamiNvidiaDeviceProduct(labels, device)
		allocatable := hamiNvidiaDeviceAllocatable(device)
		used := usage[device.ID]
		available := hamiNvidiaDeviceAvailable(allocatable, used)

		deviceResources = append(deviceResources, &v1.DeviceResource{
			UUID:        device.ID,
			Product:     product,
			Health:      device.healthy(),
			Allocatable: allocatable,
			Available:   available,
		})

		if !device.healthy() {
			continue
		}

		productKey := v1.AcceleratorProduct(product)
		addHAMiNvidiaProductResource(allocatableGroup, productKey, 1, allocatable)
		if hamiNvidiaDeviceHasAvailableCapacity(available) {
			addHAMiNvidiaProductResource(availableGroup, productKey, 1, available)
		}

		if _, exists := metadata.Products[productKey]; !exists && allocatable.MemoryMiB > 0 {
			metadata.Products[productKey] = &v1.AcceleratorProductMetadata{
				MemoryTotalMiB: float64(allocatable.MemoryMiB),
			}
		}
	}

	return &resourceview.KubernetesResourceAdapterResult{
		Allocatable: &v1.ResourceInfo{
			AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
				v1.AcceleratorTypeNVIDIAGPU: allocatableGroup,
			},
		},
		Available: &v1.ResourceInfo{
			AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
				v1.AcceleratorTypeNVIDIAGPU: availableGroup,
			},
		},
		Devices: deviceResources,
		AcceleratorMetadata: map[v1.AcceleratorType]*v1.AcceleratorMetadata{
			v1.AcceleratorTypeNVIDIAGPU: metadata,
		},
	}
}

func hamiNvidiaDeviceProduct(labels map[string]string, device hamiNvidiaRegisteredDevice) string {
	if product := labels[NvidiaGPUKubernetesNodeSelectorKey]; product != "" {
		return product
	}
	if device.Type != "" {
		return device.Type
	}

	return "unknown"
}

func hamiNvidiaDeviceAllocatable(device hamiNvidiaRegisteredDevice) *v1.DeviceResourcePool {
	return &v1.DeviceResourcePool{
		MemoryMiB: nonZeroInt64(device.DevMem, 0),
		CoreUnits: nonZeroInt64(device.DevCore, hamiDefaultNvidiaDeviceCoreUnits),
	}
}

func hamiNvidiaDeviceAvailable(
	allocatable *v1.DeviceResourcePool,
	used hamiNvidiaDeviceUsage,
) *v1.DeviceResourcePool {
	return &v1.DeviceResourcePool{
		MemoryMiB: nonNegativeInt64(allocatable.MemoryMiB - used.memoryMiB),
		CoreUnits: nonNegativeInt64(allocatable.CoreUnits - used.coreUnits),
	}
}

func hamiNvidiaDeviceHasAvailableCapacity(pool *v1.DeviceResourcePool) bool {
	return pool != nil && pool.MemoryMiB > 0 && pool.CoreUnits > 0
}

func addHAMiNvidiaProductResource(
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

func newNvidiaAcceleratorGroup() *v1.AcceleratorGroup {
	return &v1.AcceleratorGroup{
		ProductGroups: make(map[v1.AcceleratorProduct]float64),
		Products:      make(map[v1.AcceleratorProduct]*v1.AcceleratorProductResource),
	}
}

func (d hamiNvidiaRegisteredDevice) healthy() bool {
	return d.Health == nil || *d.Health
}

func nonZeroInt64(value, fallback int64) int64 {
	if value == 0 {
		return fallback
	}

	return value
}

func nonNegativeInt64(value int64) int64 {
	if value < 0 {
		return 0
	}

	return value
}
