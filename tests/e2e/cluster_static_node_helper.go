package e2e

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/gomega"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/semver"
)

func usesStaticNodeClusterFlow(version string) bool {
	enabled, err := semver.LessThan(v1.StaticNodeClusterFlowVersionGate, version)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "cluster version must be semver")

	return enabled
}

func expectedStaticNodeIPs(headIP string, workerIPs []string) []string {
	ips := []string{headIP}

	return append(ips, workerIPs...)
}

func rayContainerListCommand(clusterName string, role v1.StaticNodeRole, all bool) string {
	ps := "docker ps"
	if all {
		ps = "docker ps -a"
	}

	if !usesStaticNodeClusterFlow(profileClusterVersion()) {
		return ps + " --filter name=ray_container --format '{{.Names}}'"
	}

	command := fmt.Sprintf(
		"%s --filter label='neutree.ai/static-node-cluster=%s'",
		ps,
		clusterName,
	)
	if role != "" {
		command += fmt.Sprintf(" --filter label='neutree.ai/component=%s'", rayComponentName(role))
	}

	return command + " --format '{{.Names}}'"
}

func rayContainerNameCommand(clusterName string, role v1.StaticNodeRole) string {
	return rayContainerListCommand(clusterName, role, false) + " | head -n 1"
}

func rayContainerBackgroundExecCommand(clusterName string, role v1.StaticNodeRole, command string) string {
	return fmt.Sprintf(
		"container=$(%s); test -n \"$container\" && nohup docker exec \"$container\" %s > /dev/null 2>&1 &",
		rayContainerNameCommand(clusterName, role),
		command,
	)
}

func rayComponentName(role v1.StaticNodeRole) string {
	if role == v1.StaticNodeRoleHead {
		return "ray-head"
	}

	return "ray-worker"
}

func eventuallyStaticNodeClusterReady(name string, desiredVersion string, desiredNodes int) {
	Eventually(func(g Gomega) {
		r := RunCLI("get", "StaticNodeCluster", name, "-w", profileWorkspace(), "-o", "json")
		g.Expect(r.ExitCode).To(Equal(0), r.Stderr)

		var cluster v1.StaticNodeCluster
		g.Expect(json.Unmarshal([]byte(r.Stdout), &cluster)).To(Succeed())
		g.Expect(cluster.Status).NotTo(BeNil())
		g.Expect(cluster.Status.Phase).To(Equal(v1.StaticNodeClusterPhaseReady))
		g.Expect(cluster.Status.DesiredNodes).To(Equal(desiredNodes))
		g.Expect(cluster.Status.ReadyNodes).To(Equal(desiredNodes))
		g.Expect(cluster.Status.HeadReady).To(BeTrue())
		g.Expect(cluster.Status.Version).To(Equal(desiredVersion))
	}, TerminalPhaseTimeout, 5*time.Second).Should(Succeed())
}

func assertNoStaticNodeCluster(name string) {
	r := RunCLI("get", "StaticNodeCluster", "-w", profileWorkspace(), "-o", "json")
	if r.ExitCode != 0 {
		return
	}

	if strings.TrimSpace(r.Stdout) == "" || strings.Contains(r.Stdout, "No staticnodecluster resources found") {
		return
	}

	clusters := parseStaticNodeClusterList(r.Stdout)

	for _, cluster := range clusters {
		if cluster.Metadata != nil {
			Expect(cluster.Metadata.Name).NotTo(Equal(name))
		}
	}
}

func assertStaticNodesForCluster(clusterName string, expectedIPs []string) {
	nodes := getStaticNodesForCluster(clusterName)

	if len(expectedIPs) == 0 {
		if len(nodes) == 0 {
			return
		}
	}

	actual := map[string]struct{}{}

	for _, node := range nodes {
		actual[node.Spec.IP] = struct{}{}
	}

	Expect(actual).To(HaveLen(len(expectedIPs)))

	for _, ip := range expectedIPs {
		Expect(actual).To(HaveKey(ip))
	}
}

func assertStaticNodeMetricsComponents(clusterName string) {
	nodes := getStaticNodesForCluster(clusterName)
	ExpectWithOffset(1, nodes).NotTo(BeEmpty())

	hasGPUNode := false
	for _, node := range nodes {
		ExpectWithOffset(1, node.Spec).NotTo(BeNil())
		ExpectWithOffset(1, node.Status).NotTo(BeNil())

		nodeExporter := requireStaticNodeComponent(node, "node-exporter")
		ExpectWithOffset(1, nodeExporter.Ports).To(ContainElement(v1.NodeComponentPort{
			Name:     "metrics",
			Port:     19100,
			Protocol: "TCP",
		}))
		requireStaticNodeComponentRunning(node, "node-exporter")

		if node.Spec.Role == v1.StaticNodeRoleHead {
			vmagent := requireStaticNodeComponent(node, "vmagent")
			requireStaticNodeComponentRunning(node, "vmagent")
			vmagentConfig := requireStaticNodeComponentConfigFile(vmagent, "/etc/neutree/vmagent/config.yaml")
			ExpectWithOffset(1, vmagentConfig.Content).To(ContainSubstring("job_name: static-node-node-exporter"))
			ExpectWithOffset(1, vmagentConfig.Content).To(ContainSubstring("job_name: static-node-ray"))
		}

		isGPUNode := node.Status.Accelerator != nil &&
			node.Status.Accelerator.Type == v1.AcceleratorTypeNVIDIAGPU.String()
		if !isGPUNode {
			ExpectWithOffset(1, findStaticNodeComponent(node.Spec.Components, "accelerator-exporter")).To(BeNil())
			ExpectWithOffset(1, findStaticNodeComponentStatus(node.Status.Components, "accelerator-exporter")).To(BeNil())
			continue
		}

		hasGPUNode = true
		exporter := requireStaticNodeComponent(node, "accelerator-exporter")
		ExpectWithOffset(1, exporter.Ports).To(ContainElement(v1.NodeComponentPort{
			Name:     "metrics",
			Port:     19400,
			Protocol: "TCP",
		}))
		requireStaticNodeComponentRunning(node, "accelerator-exporter")
	}

	if hasGPUNode {
		head := requireStaticNodeRole(nodes, v1.StaticNodeRoleHead)
		vmagent := requireStaticNodeComponent(head, "vmagent")
		vmagentConfig := requireStaticNodeComponentConfigFile(vmagent, "/etc/neutree/vmagent/config.yaml")
		ExpectWithOffset(1, vmagentConfig.Content).To(ContainSubstring("job_name: accelerator-exporter-nvidia-gpu"))
	}
}

func assertStaticNodeExternalAcceleratorExporterComponents(clusterName string) {
	nodes := getStaticNodesForCluster(clusterName)
	ExpectWithOffset(1, nodes).NotTo(BeEmpty())

	gpuNodeIPs := []string{}
	for _, node := range nodes {
		ExpectWithOffset(1, node.Spec).NotTo(BeNil())
		ExpectWithOffset(1, node.Status).NotTo(BeNil())

		nodeExporter := requireStaticNodeComponent(node, "node-exporter")
		ExpectWithOffset(1, nodeExporter.Ports).To(ContainElement(v1.NodeComponentPort{
			Name:     "metrics",
			Port:     19100,
			Protocol: "TCP",
		}))
		requireStaticNodeComponentRunning(node, "node-exporter")

		ExpectWithOffset(1, findStaticNodeComponent(node.Spec.Components, "accelerator-exporter")).To(BeNil())
		ExpectWithOffset(1, findStaticNodeComponentStatus(node.Status.Components, "accelerator-exporter")).To(BeNil())

		if node.Status.Accelerator != nil &&
			node.Status.Accelerator.Type == v1.AcceleratorTypeNVIDIAGPU.String() {
			gpuNodeIPs = append(gpuNodeIPs, node.Spec.IP)
		}
	}

	head := requireStaticNodeRole(nodes, v1.StaticNodeRoleHead)
	vmagent := requireStaticNodeComponent(head, "vmagent")
	requireStaticNodeComponentRunning(head, "vmagent")
	vmagentConfig := requireStaticNodeComponentConfigFile(vmagent, "/etc/neutree/vmagent/config.yaml")
	ExpectWithOffset(1, vmagentConfig.Content).To(ContainSubstring("job_name: static-node-node-exporter"))
	ExpectWithOffset(1, vmagentConfig.Content).To(ContainSubstring("job_name: static-node-ray"))

	if len(gpuNodeIPs) == 0 {
		ExpectWithOffset(1, vmagentConfig.Content).NotTo(ContainSubstring("job_name: static-node-accelerator-exporter"))
		return
	}

	ExpectWithOffset(1, vmagentConfig.Content).To(ContainSubstring("job_name: static-node-accelerator-exporter"))
	acceleratorTargets := requireStaticNodeComponentConfigFile(
		vmagent,
		"/etc/neutree/vmagent/file_sd/accelerator-exporter.json",
	)
	for _, ip := range gpuNodeIPs {
		ExpectWithOffset(1, acceleratorTargets.Content).To(ContainSubstring(fmt.Sprintf(`"%s:9400"`, ip)))
	}
}

func getStaticNodesForCluster(clusterName string) []v1.StaticNode {
	r := RunCLI("get", "StaticNode", "-w", profileWorkspace(), "-o", "json")
	if r.ExitCode != 0 || strings.Contains(r.Stdout, "No staticnode resources found") {
		return nil
	}

	nodes := parseStaticNodeList(r.Stdout)
	filtered := make([]v1.StaticNode, 0, len(nodes))
	for _, node := range nodes {
		if node.Spec == nil || node.Spec.Cluster != clusterName {
			continue
		}

		filtered = append(filtered, node)
	}

	return filtered
}

func requireStaticNodeRole(nodes []v1.StaticNode, role v1.StaticNodeRole) v1.StaticNode {
	for _, node := range nodes {
		if node.Spec != nil && node.Spec.Role == role {
			return node
		}
	}

	ExpectWithOffset(1, false).To(BeTrue(), "expected static node with role %s", role)

	return v1.StaticNode{}
}

func requireStaticNodeComponent(node v1.StaticNode, name string) v1.NodeComponentSpec {
	if node.Spec == nil {
		ExpectWithOffset(1, node.Spec).NotTo(BeNil(), "static node spec is nil")
	}

	component := findStaticNodeComponent(node.Spec.Components, name)
	if component == nil {
		ExpectWithOffset(1, component).NotTo(BeNil(),
			"expected static node %s to have component %s", staticNodeName(node), name)
	}

	return *component
}

func findStaticNodeComponent(components []v1.NodeComponentSpec, name string) *v1.NodeComponentSpec {
	for i := range components {
		if components[i].Name == name {
			return &components[i]
		}
	}

	return nil
}

func requireStaticNodeComponentRunning(node v1.StaticNode, name string) {
	if node.Status == nil {
		ExpectWithOffset(1, node.Status).NotTo(BeNil(), "static node status is nil")
	}

	status := findStaticNodeComponentStatus(node.Status.Components, name)
	if status == nil {
		ExpectWithOffset(1, status).NotTo(BeNil(),
			"expected static node %s to have component status %s", staticNodeName(node), name)
	}

	ExpectWithOffset(1, status.Ready).To(BeTrue(), "component %s should be ready on %s", name, staticNodeName(node))
	ExpectWithOffset(1, status.Phase).To(Equal(v1.NodeComponentPhaseRunning), "component %s should be running on %s", name, staticNodeName(node))
	ExpectWithOffset(1, status.Message).To(BeEmpty(), "component %s should not report errors on %s", name, staticNodeName(node))
}

func findStaticNodeComponentStatus(statuses []v1.NodeComponentStatus, name string) *v1.NodeComponentStatus {
	for i := range statuses {
		if statuses[i].Name == name {
			return &statuses[i]
		}
	}

	return nil
}

func requireStaticNodeComponentConfigFile(
	component v1.NodeComponentSpec,
	path string,
) v1.NodeComponentConfigFile {
	for _, configFile := range component.ConfigFiles {
		if configFile.Path == path {
			return configFile
		}
	}

	ExpectWithOffset(1, false).To(BeTrue(),
		"expected component %s to have config file %s", component.Name, path)

	return v1.NodeComponentConfigFile{}
}

func staticNodeName(node v1.StaticNode) string {
	if node.Metadata == nil {
		return ""
	}

	return node.Metadata.Name
}

func parseStaticNodeClusterList(stdout string) []v1.StaticNodeCluster {
	var clusters []v1.StaticNodeCluster
	if err := json.Unmarshal([]byte(stdout), &clusters); err == nil {
		return clusters
	}

	var cluster v1.StaticNodeCluster
	ExpectWithOffset(1, json.Unmarshal([]byte(stdout), &cluster)).To(Succeed())

	return []v1.StaticNodeCluster{cluster}
}

func parseStaticNodeList(stdout string) []v1.StaticNode {
	var nodes []v1.StaticNode
	if err := json.Unmarshal([]byte(stdout), &nodes); err == nil {
		return nodes
	}

	var node v1.StaticNode
	ExpectWithOffset(1, json.Unmarshal([]byte(stdout), &node)).To(Succeed())

	return []v1.StaticNode{node}
}
