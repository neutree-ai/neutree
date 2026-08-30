package nvidia

import (
	"cmp"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

// nvidiaKubernetesAllocations resolves PodResources and HAMi evidence into
// NVIDIA allocations without letting the generic NodeAgent host interpret
// NVIDIA resource names or annotations.
func nvidiaKubernetesAllocations(
	hardwareSnapshot adapter.HardwareSnapshot,
	evidence adapter.KubernetesEvidence,
) ([]v1.StaticNodeAllocationStatus, error) {
	if !evidence.AllocationAvailable {
		return nil, nil
	}

	podResources := make(map[string]adapter.PodResource, len(evidence.PodResources))
	for _, podResource := range evidence.PodResources {
		podResources[podResource.Namespace+"/"+podResource.Name] = podResource
	}

	lookup := newNvidiaDeviceLookup(hardwareSnapshot.Accelerator.Devices)

	products, err := nvidiaHAMiDeviceProducts(evidence, hardwareSnapshot)
	if err != nil {
		return nil, err
	}

	allocations := make([]v1.StaticNodeAllocationStatus, 0, len(evidence.EndpointPods))

	for _, pod := range evidence.EndpointPods {
		if pod.Labels["endpoint"] == "" {
			continue
		}

		nodeID := cmp.Or(evidence.Common.Labels.Node, pod.NodeName)

		devices, err := nvidiaHAMiDeviceAllocations(
			pod.Annotations[nvidiaHAMiVGPUDevicesAllocated],
			nodeID,
			products,
		)
		if err != nil {
			return nil, err
		}

		if len(devices) == 0 {
			podResource, ok := podResources[pod.Namespace+"/"+pod.Name]
			if !ok {
				continue
			}

			refs := make([]string, 0)
			for _, container := range podResource.Containers {
				refs = append(refs, container.DeviceIDs...)
			}

			devices = nvidiaAllocationDevices(refs, lookup, nodeID, 0)
		}

		if len(devices) == 0 {
			continue
		}

		allocations = append(allocations, v1.StaticNodeAllocationStatus{
			WorkloadType: "endpoint",
			Workspace:    pod.Labels[v1.NeutreeClusterWorkspaceLabelKey],
			Endpoint:     pod.Labels["endpoint"],
			InstanceID:   pod.Name,
			ReplicaID:    pod.Name,
			RuntimeID:    pod.Namespace + "/" + pod.Name,
			Devices:      devices,
		})
	}

	sortAllocations(allocations)

	return allocations, nil
}
