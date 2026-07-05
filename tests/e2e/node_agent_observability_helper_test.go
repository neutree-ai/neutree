package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator/resourceparser"
)

var endpointAcceleratorMetricLabelNames = []string{
	"cluster_type",
	"endpoint",
	"instance_id",
	"replica",
	"node",
	"accelerator_type",
	"accelerator_uuid",
	"accelerator_index",
	"vdevice_index",
	"product",
}

var forbiddenEndpointAcceleratorMetricLabels = []string{
	"workspace",
	"neutree_cluster",
	"source",
	"node_ip",
	"container",
	"container_id",
	"replica_id",
	"gpu_uuid",
	"gpu_index",
}

var obsoleteEndpointAcceleratorMetricNames = []string{
	"neutree_endpoint_replica_gpu_allocation",
	"neutree_endpoint_replica_gpu_memory_allocated_bytes",
	"neutree_endpoint_replica_gpu_memory_used_bytes",
	"neutree_endpoint_replica_gpu_utilization_ratio",
	"neutree_node_gpu_allocation_info",
}

func assertK8sNodeAgentEndpointAcceleratorMetrics(clusterName, endpointName string) {
	assertK8sNodeAgentEndpointAcceleratorMetricsWithVDeviceIndex(clusterName, endpointName, "0")
}

func assertK8sNodeAgentEndpointAcceleratorMetricsWithVDeviceIndex(
	clusterName string,
	endpointName string,
	expectedVDeviceIndex string,
) {
	ctx := context.Background()
	k8sH := NewK8sHelper(profileKubeconfig())
	namespace := k8sClusterNamespace(clusterName)

	metrics := eventuallyNodeAgentMetricsSatisfy(ctx, k8sH, namespace, func(body string) error {
		return validateEndpointAcceleratorMetricContract(body, endpointName, expectedVDeviceIndex)
	})
	ExpectWithOffset(1, metrics).NotTo(BeEmpty())
}

func assertStaticRayNodeAgentEndpointAcceleratorMetrics(clusterName, endpointName string) {
	nodes := getStaticNodesForCluster(clusterName)
	ExpectWithOffset(1, nodes).NotTo(BeEmpty(), "static nodes should exist for cluster %s", clusterName)

	sshUser := profileSSHUser()
	if sshUser == "" {
		sshUser = "root"
	}
	ExpectWithOffset(1, profile.SSHNodes).NotTo(BeEmpty(), "ssh_nodes must be configured")

	keyFile := expandHome(profile.SSHNodes[0].KeyFile)
	ExpectWithOffset(1, keyFile).NotTo(BeEmpty(), "ssh key file must be configured")

	EventuallyWithOffset(1, func(g Gomega) {
		var lastBodies []string
		var lastErrs []string
		for _, node := range nodes {
			if node.Spec == nil || node.Spec.IP == "" {
				continue
			}

			result := RunSSH(sshUser, node.Spec.IP, keyFile,
				"curl -fsS --max-time 5 http://127.0.0.1:19101/metrics")
			if result.ExitCode != 0 {
				lastErrs = append(lastErrs, fmt.Sprintf("%s: %s", staticNodeName(node), result.Stderr))
				continue
			}

			lastBodies = append(lastBodies, result.Stdout)
			if err := validateEndpointAcceleratorMetricContract(result.Stdout, endpointName, ""); err == nil {
				return
			} else {
				lastErrs = append(lastErrs, fmt.Sprintf("%s: %v", staticNodeName(node), err))
			}
		}

		g.Expect(strings.Join(lastBodies, "\n")).NotTo(BeEmpty(),
			"static node-agent metrics should be reachable, errors: %s", strings.Join(lastErrs, "; "))
		g.Expect(strings.Join(lastErrs, "; ")).To(BeEmpty(),
			"static node-agent metrics should contain endpoint accelerator samples")
	}, TerminalPhaseTimeout, 5*time.Second).Should(Succeed())
}

func assertStaticRayEndpointAcceleratorResourceSync(clusterName, endpointName string) {
	snapshotAllocations := eventuallyStaticRayNodeAgentEndpointAllocations(clusterName, endpointName)
	ExpectWithOffset(1, snapshotAllocations).NotTo(BeEmpty(),
		"node-agent device snapshot should expose endpoint allocations for %s", endpointName)

	staticNodeAllocations := eventuallyStaticNodeEndpointAllocations(clusterName, endpointName)
	ExpectWithOffset(1, staticNodeAllocations).NotTo(BeEmpty(),
		"StaticNode.status.allocations should persist endpoint allocations for %s", endpointName)

	assertAllocationDeviceSetContains(staticNodeAllocations, snapshotAllocations)

	cluster := eventuallyStaticRayClusterResourcesReflectAllocations(clusterName, staticNodeAllocations)
	assertEndpointResourcesReflectAllocations(endpointName, cluster, staticNodeAllocations)
}

func eventuallyStaticRayNodeAgentEndpointAllocations(
	clusterName string,
	endpointName string,
) []v1.StaticNodeAllocationStatus {
	nodes := getStaticNodesForCluster(clusterName)
	ExpectWithOffset(1, nodes).NotTo(BeEmpty(), "static nodes should exist for cluster %s", clusterName)

	sshUser := profileSSHUser()
	if sshUser == "" {
		sshUser = "root"
	}
	ExpectWithOffset(1, profile.SSHNodes).NotTo(BeEmpty(), "ssh_nodes must be configured")

	keyFile := expandHome(profile.SSHNodes[0].KeyFile)
	ExpectWithOffset(1, keyFile).NotTo(BeEmpty(), "ssh key file must be configured")

	var matched []v1.StaticNodeAllocationStatus
	EventuallyWithOffset(1, func(g Gomega) {
		var errors []string
		current := make([]v1.StaticNodeAllocationStatus, 0, len(matched))
		for _, node := range nodes {
			if node.Spec == nil || node.Spec.IP == "" {
				continue
			}

			result := RunSSH(sshUser, node.Spec.IP, keyFile,
				"curl -fsS --max-time 5 http://127.0.0.1:19101/v1/node/device-snapshot")
			if result.ExitCode != 0 {
				errors = append(errors, fmt.Sprintf("%s: %s", staticNodeName(node), result.Stderr))
				continue
			}

			var snapshot v1.NodeDeviceSnapshot
			if err := json.Unmarshal([]byte(result.Stdout), &snapshot); err != nil {
				errors = append(errors, fmt.Sprintf("%s: %v", staticNodeName(node), err))
				continue
			}

			current = append(current, endpointAllocations(snapshot.Allocations, endpointName)...)
		}

		matched = current
		g.Expect(strings.Join(errors, "; ")).To(BeEmpty(),
			"node-agent device snapshots should contain endpoint allocations for %s", endpointName)
		g.Expect(matched).NotTo(BeEmpty(),
			"node-agent device snapshots should contain endpoint allocations for %s", endpointName)
	}, TerminalPhaseTimeout, 5*time.Second).Should(Succeed())

	return append([]v1.StaticNodeAllocationStatus(nil), matched...)
}

func eventuallyStaticNodeEndpointAllocations(
	clusterName string,
	endpointName string,
) []v1.StaticNodeAllocationStatus {
	var matched []v1.StaticNodeAllocationStatus

	EventuallyWithOffset(1, func(g Gomega) {
		nodes := getStaticNodesForCluster(clusterName)
		g.Expect(nodes).NotTo(BeEmpty(), "static nodes should exist for cluster %s", clusterName)

		matched = matched[:0]
		for _, node := range nodes {
			if node.Status == nil {
				continue
			}
			matched = append(matched, endpointAllocations(node.Status.Allocations, endpointName)...)
		}

		g.Expect(matched).NotTo(BeEmpty(),
			"StaticNode.status.allocations should include endpoint %s", endpointName)
	}, TerminalPhaseTimeout, 5*time.Second).Should(Succeed())

	return append([]v1.StaticNodeAllocationStatus(nil), matched...)
}

func eventuallyStaticRayClusterResourcesReflectAllocations(
	clusterName string,
	allocations []v1.StaticNodeAllocationStatus,
) v1.Cluster {
	var cluster v1.Cluster

	EventuallyWithOffset(1, func(g Gomega) {
		cluster = getClusterFullJSON(clusterName)
		g.Expect(cluster.Status).NotTo(BeNil())
		g.Expect(cluster.Status.ResourceInfo).NotTo(BeNil())
		g.Expect(cluster.Status.ResourceInfo.NodeResources).NotTo(BeEmpty())

		for _, allocation := range allocations {
			for _, device := range allocation.Devices {
				if device.UUID == "" || device.NodeID == "" {
					continue
				}

				nodeResources := cluster.Status.ResourceInfo.NodeResources[device.NodeID]
				g.Expect(nodeResources).NotTo(BeNil(),
					"cluster resource node %s should exist for allocation %s", device.NodeID, device.UUID)

				clusterDevice := findClusterDevice(nodeResources.Devices, device.UUID)
				g.Expect(clusterDevice).NotTo(BeNil(),
					"cluster resource device %s should exist on node %s", device.UUID, device.NodeID)
				g.Expect(clusterDevice.Allocatable).NotTo(BeNil(),
					"cluster resource device %s should include allocatable", device.UUID)
				g.Expect(clusterDevice.Available).NotTo(BeNil(),
					"cluster resource device %s should include available", device.UUID)
				g.Expect(clusterDevice.Available.CoreUnits).To(BeNumerically("<", clusterDevice.Allocatable.CoreUnits),
					"allocated device %s should reduce available core units", device.UUID)
				g.Expect(clusterDevice.Available.MemoryMiB).To(BeNumerically("<", clusterDevice.Allocatable.MemoryMiB),
					"allocated device %s should reduce available memory", device.UUID)
			}
		}
	}, TerminalPhaseTimeout, 5*time.Second).Should(Succeed())

	return cluster
}

func assertEndpointResourcesReflectAllocations(
	endpointName string,
	cluster v1.Cluster,
	allocations []v1.StaticNodeAllocationStatus,
) {
	var endpoint v1.Endpoint

	EventuallyWithOffset(1, func(g Gomega) {
		endpoint = getEndpoint(endpointName)
		g.Expect(endpoint.Status).NotTo(BeNil())
		g.Expect(endpoint.Status.Resources).NotTo(BeNil(),
			"endpoint %s status.resources should be populated", endpointName)
		g.Expect(endpoint.Status.Resources.Replicas).NotTo(BeEmpty(),
			"endpoint %s status.resources.replicas should be populated", endpointName)
		g.Expect(endpoint.Status.Resources.Summary).NotTo(BeNil(),
			"endpoint %s status.resources.summary should be populated", endpointName)
		g.Expect(endpoint.Status.Resources.Summary.Products).NotTo(BeEmpty(),
			"endpoint %s status.resources.summary.products should be populated", endpointName)

		for _, allocation := range allocations {
			replica := findEndpointResourceReplica(endpoint.Status.Resources.Replicas, allocation.ReplicaID)
			g.Expect(replica).NotTo(BeNil(),
				"endpoint %s resources should include replica %s", endpointName, allocation.ReplicaID)
			g.Expect(replica.InstanceID).To(Equal(allocation.ReplicaID),
				"endpoint %s replica %s should use replica id as instance id", endpointName, allocation.ReplicaID)
			g.Expect(replica.NodeID).NotTo(BeEmpty(),
				"endpoint %s replica %s should include node id", endpointName, allocation.ReplicaID)
			g.Expect(replica.Devices).NotTo(BeEmpty(),
				"endpoint %s replica %s should include devices", endpointName, allocation.ReplicaID)

			for _, allocated := range allocation.Devices {
				device := findEndpointResourceDevice(replica.Devices, allocated.UUID)
				g.Expect(device).NotTo(BeNil(),
					"endpoint %s replica %s should include device %s",
					endpointName, allocation.ReplicaID, allocated.UUID)
				g.Expect(device.Product).NotTo(BeEmpty(),
					"endpoint %s device %s should include product", endpointName, allocated.UUID)
				g.Expect(device.MemoryMiB).To(BeNumerically(">", 0),
					"endpoint %s device %s should include memory", endpointName, allocated.UUID)
				g.Expect(device.CoreUnits).To(BeNumerically(">", 0),
					"endpoint %s device %s should include core units", endpointName, allocated.UUID)

				clusterDevice := clusterDeviceForAllocation(cluster, allocated)
				if clusterDevice != nil && clusterDevice.Order != nil {
					g.Expect(device.Order).NotTo(BeNil(),
						"endpoint %s device %s should include cluster device order", endpointName, allocated.UUID)
					g.Expect(*device.Order).To(Equal(*clusterDevice.Order),
						"endpoint %s device %s order should match cluster resource order", endpointName, allocated.UUID)
				}
			}
		}
	}, TerminalPhaseTimeout, 5*time.Second).Should(Succeed())
}

func endpointAllocations(
	allocations []v1.StaticNodeAllocationStatus,
	endpointName string,
) []v1.StaticNodeAllocationStatus {
	result := make([]v1.StaticNodeAllocationStatus, 0, len(allocations))
	for _, allocation := range allocations {
		if allocation.Endpoint != endpointName || len(allocation.Devices) == 0 {
			continue
		}
		result = append(result, allocation)
	}

	return result
}

func assertAllocationDeviceSetContains(
	actual []v1.StaticNodeAllocationStatus,
	expected []v1.StaticNodeAllocationStatus,
) {
	actualDevices := allocationDeviceSet(actual)
	for key := range allocationDeviceSet(expected) {
		ExpectWithOffset(1, actualDevices).To(HaveKey(key),
			"StaticNode.status.allocations should contain node-agent allocation device %s", key)
	}
}

func allocationDeviceSet(allocations []v1.StaticNodeAllocationStatus) map[string]struct{} {
	result := map[string]struct{}{}
	for _, allocation := range allocations {
		for _, device := range allocation.Devices {
			result[allocation.Endpoint+"|"+allocation.ReplicaID+"|"+device.NodeID+"|"+device.UUID] = struct{}{}
		}
	}

	return result
}

func findClusterDevice(devices []*v1.DeviceResource, uuid string) *v1.DeviceResource {
	for _, device := range devices {
		if device != nil && device.UUID == uuid {
			return device
		}
	}

	return nil
}

func clusterDeviceForAllocation(cluster v1.Cluster, allocation v1.DeviceAllocation) *v1.DeviceResource {
	if cluster.Status == nil || cluster.Status.ResourceInfo == nil {
		return nil
	}

	node := cluster.Status.ResourceInfo.NodeResources[allocation.NodeID]
	if node == nil {
		return nil
	}

	return findClusterDevice(node.Devices, allocation.UUID)
}

func findEndpointResourceReplica(
	replicas []v1.ReplicaDeviceAllocation,
	replicaID string,
) *v1.ReplicaDeviceAllocation {
	for i := range replicas {
		if replicas[i].ReplicaID == replicaID {
			return &replicas[i]
		}
	}

	return nil
}

func findEndpointResourceDevice(devices []v1.DeviceAllocation, uuid string) *v1.DeviceAllocation {
	for i := range devices {
		if devices[i].UUID == uuid {
			return &devices[i]
		}
	}

	return nil
}

func assertK8sEndpointAcceleratorAllocationAnnotations(clusterName, endpointName string) {
	ctx := context.Background()
	k8sH := NewK8sHelper(profileKubeconfig())
	namespace := k8sClusterNamespace(clusterName)

	EventuallyWithOffset(1, func(g Gomega) {
		pods, err := k8sH.ListPods(ctx, namespace, "endpoint="+endpointName)
		g.Expect(err).NotTo(HaveOccurred(), "should list endpoint pods")
		g.Expect(pods).NotTo(BeEmpty(), "endpoint pods should exist")

		for _, pod := range pods {
			if validateEndpointAllocationAnnotation(pod.Annotations) == nil {
				return
			}
		}

		g.Expect(fmt.Sprintf("endpoint %s allocation annotation", endpointName)).To(Equal("present"))
	}, TerminalPhaseTimeout, 5*time.Second).Should(Succeed())
}

func assertK8sNodeAcceleratorDeviceAnnotations(ctx context.Context, k8sH *K8sHelper) {
	EventuallyWithOffset(1, func(g Gomega) {
		nodes, err := k8sH.ListNodes(ctx, "nvidia.com/gpu.present=true")
		g.Expect(err).NotTo(HaveOccurred(), "should list NVIDIA GPU nodes")
		g.Expect(nodes).NotTo(BeEmpty(), "GPU nodes should exist")

		var missing []string
		for _, node := range nodes {
			if err := validateNodeDeviceAnnotation(node.Annotations); err != nil {
				missing = append(missing, fmt.Sprintf("%s: %v", node.Name, err))
			}
		}

		g.Expect(missing).To(BeEmpty(), "all GPU nodes should have accelerator device annotations")
	}, TerminalPhaseTimeout, 5*time.Second).Should(Succeed())
}

func k8sClusterNamespace(clusterName string) string {
	cluster := getClusterFullJSON(clusterName)
	ExpectWithOffset(1, cluster.Metadata).NotTo(BeNil())

	return ClusterNamespace(cluster.Metadata.Workspace, cluster.Metadata.Name, cluster.ID)
}

func eventuallyNodeAgentMetricsSatisfy(
	ctx context.Context,
	k8sH *K8sHelper,
	namespace string,
	validate func(string) error,
) string {
	var matched string
	var lastBodies []string
	var lastErrs []string

	EventuallyWithOffset(1, func(g Gomega) {
		pods, err := k8sH.ListPods(ctx, namespace, "app=neutree-node-agent")
		g.Expect(err).NotTo(HaveOccurred(), "should list neutree-node-agent pods")
		g.Expect(pods).NotTo(BeEmpty(), "neutree-node-agent should have pods")

		lastBodies = lastBodies[:0]
		lastErrs = lastErrs[:0]
		for _, pod := range pods {
			if pod.Status.Phase != corev1.PodRunning {
				continue
			}

			raw, err := k8sH.PodProxyGetRaw(ctx, namespace, pod.Name, "19101", "/metrics")
			if err != nil {
				continue
			}

			body := string(raw)
			lastBodies = append(lastBodies, body)
			if err := validate(body); err == nil {
				matched = body
				return
			} else {
				lastErrs = append(lastErrs, err.Error())
			}
		}

		g.Expect(strings.Join(lastBodies, "\n")).NotTo(BeEmpty(),
			"node-agent metrics should satisfy contract, validation errors: %s", strings.Join(lastErrs, "; "))
		g.Expect(strings.Join(lastErrs, "; ")).To(BeEmpty(),
			"node-agent metrics should satisfy contract")
	}, TerminalPhaseTimeout, 5*time.Second).Should(Succeed())

	return matched
}

func validateEndpointAcceleratorMetricContract(body, endpointName, expectedVDeviceIndex string) error {
	for _, name := range obsoleteEndpointAcceleratorMetricNames {
		if strings.Contains(body, name) {
			return fmt.Errorf("obsolete metric %s is present", name)
		}
	}

	families, err := (&expfmt.TextParser{}).TextToMetricFamilies(strings.NewReader(body))
	if err != nil {
		return fmt.Errorf("parse node-agent metrics: %w", err)
	}

	required := []string{
		"neutree_endpoint_replica_accelerator_allocation",
		"neutree_endpoint_replica_accelerator_memory_allocated_bytes",
		"neutree_endpoint_replica_accelerator_memory_used_bytes",
		"neutree_endpoint_replica_accelerator_utilization_ratio",
	}
	for _, name := range required {
		family := families[name]
		if family == nil {
			return fmt.Errorf("metric %s is missing", name)
		}
		if err := validateEndpointAcceleratorMetricFamily(name, endpointName, expectedVDeviceIndex, family); err != nil {
			return err
		}
	}

	return nil
}

func validateEndpointAcceleratorMetricFamily(
	metricName string,
	endpointName string,
	expectedVDeviceIndex string,
	family *dto.MetricFamily,
) error {
	for _, metric := range family.GetMetric() {
		labels := map[string]string{}
		for _, pair := range metric.GetLabel() {
			labels[pair.GetName()] = pair.GetValue()
		}
		if labels["endpoint"] != endpointName {
			continue
		}

		if got, want := sortedMapKeys(labels), endpointAcceleratorMetricLabelNames; !stringSlicesEqual(got, sortedStrings(want)) {
			return fmt.Errorf("metric %s labels = %v, want %v", metricName, got, sortedStrings(want))
		}
		for _, label := range forbiddenEndpointAcceleratorMetricLabels {
			if _, exists := labels[label]; exists {
				return fmt.Errorf("metric %s has forbidden label %s", metricName, label)
			}
		}
		for _, label := range endpointAcceleratorMetricLabelNames {
			if labels[label] == "" {
				return fmt.Errorf("metric %s label %s is empty", metricName, label)
			}
		}
		if expectedVDeviceIndex != "" && labels["vdevice_index"] != expectedVDeviceIndex {
			return fmt.Errorf("metric %s vdevice_index = %q, want %q",
				metricName, labels["vdevice_index"], expectedVDeviceIndex)
		}

		return nil
	}

	return fmt.Errorf("metric %s has no sample for endpoint %s", metricName, endpointName)
}

func validateEndpointAllocationAnnotation(annotations map[string]string) error {
	value := annotations[resourceparser.NeutreeAcceleratorAllocationsAnnotation]
	if value == "" {
		return errors.New("allocation annotation is missing")
	}

	var allocations []struct {
		UUID      string `json:"uuid"`
		Product   string `json:"product"`
		NodeID    string `json:"node_id"`
		MemoryMiB int64  `json:"memory_mib"`
		CoreUnits int64  `json:"core_units"`
	}
	if err := json.Unmarshal([]byte(value), &allocations); err != nil {
		return fmt.Errorf("parse allocation annotation: %w", err)
	}
	for _, allocation := range allocations {
		if allocation.UUID != "" &&
			allocation.Product != "" &&
			allocation.NodeID != "" &&
			allocation.MemoryMiB > 0 &&
			allocation.CoreUnits > 0 {
			return nil
		}
	}

	return errors.New("allocation annotation has no complete accelerator allocation")
}

func validateNodeDeviceAnnotation(annotations map[string]string) error {
	value := annotations[resourceparser.NeutreeAcceleratorDevicesAnnotation]
	if value == "" {
		return errors.New("device annotation is missing")
	}

	var devices []struct {
		UUID         string `json:"uuid"`
		ProductName  string `json:"product_name"`
		ProductModel string `json:"product_model"`
		MemoryMiB    int64  `json:"memory_mib"`
	}
	if err := json.Unmarshal([]byte(value), &devices); err != nil {
		return fmt.Errorf("parse device annotation: %w", err)
	}
	for _, device := range devices {
		if device.UUID != "" &&
			(device.ProductName != "" || device.ProductModel != "") &&
			device.MemoryMiB > 0 {
			return nil
		}
	}

	return errors.New("device annotation has no complete accelerator device")
}

func sortedMapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	return keys
}

func sortedStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)

	return result
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}
