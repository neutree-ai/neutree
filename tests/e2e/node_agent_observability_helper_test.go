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
