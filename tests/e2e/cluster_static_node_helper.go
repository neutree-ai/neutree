package e2e

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
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
	sshUser := profileSSHUser()

	if sshUser == "" {
		sshUser = defaultSSHUser
	}

	ExpectWithOffset(1, profile.SSHNodes).NotTo(BeEmpty(), "ssh_nodes must be configured")
	keyFile := expandHome(profile.SSHNodes[0].KeyFile)
	ExpectWithOffset(1, keyFile).NotTo(BeEmpty(), "ssh key file must be configured")

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

		nodeAgent := requireStaticNodeComponent(node, "neutree-node-agent")
		ExpectWithOffset(1, nodeAgent.Args).To(ContainElement("--listen-address=:19101"))
		ExpectWithOffset(1, nodeAgent.Ports).To(ContainElement(v1.NodeComponentPort{
			Name:     "http",
			Port:     19101,
			Protocol: "TCP",
		}))
		ExpectWithOffset(1, nodeAgent.HealthCheck).NotTo(BeNil())
		ExpectWithOffset(1, nodeAgent.HealthCheck.HTTPPath).To(Equal("/health"))
		ExpectWithOffset(1, nodeAgent.HealthCheck.Port).To(Equal(19101))
		requireStaticNodeComponentRunning(node, "neutree-node-agent")

		if node.Spec.Role == v1.StaticNodeRoleHead {
			vmagent := requireStaticNodeComponent(node, "vmagent")
			requireStaticNodeComponentRunning(node, "vmagent")

			vmagentConfig := requireStaticNodeComponentConfigFile(vmagent, "/etc/neutree/vmagent/config.yaml")
			ExpectWithOffset(1, vmagentConfig.Content).To(ContainSubstring("job_name: static-node-node-exporter"))
			ExpectWithOffset(1, vmagentConfig.Content).To(ContainSubstring("job_name: static-node-node-agent"))
			ExpectWithOffset(1, vmagentConfig.Content).To(ContainSubstring("/etc/neutree/vmagent/file_sd/node-agent.json"))
			ExpectWithOffset(1, vmagentConfig.Content).To(ContainSubstring("job_name: static-node-ray"))

			nodeAgentTargets := requireStaticNodeComponentConfigFile(vmagent, "/etc/neutree/vmagent/file_sd/node-agent.json")
			for _, target := range staticNodeTargets(nodes, 19101) {
				ExpectWithOffset(1, nodeAgentTargets.Content).To(ContainSubstring(target))
			}
		}

		isGPUNode := node.Status.Accelerator != nil &&
			node.Status.Accelerator.Type == v1.AcceleratorTypeNVIDIAGPU.String()
		assertStaticNodeAgentDeviceSnapshotAPI(node, sshUser, keyFile, isGPUNode)

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

func assertStaticNodeAgentDeviceSnapshotAPI(
	node v1.StaticNode,
	sshUser string,
	keyFile string,
	expectGPU bool,
) {
	ExpectWithOffset(1, node.Spec).NotTo(BeNil(), "static node spec is nil")
	ExpectWithOffset(1, node.Spec.IP).NotTo(BeEmpty(), "static node IP is empty")

	EventuallyWithOffset(1, func(g Gomega) {
		result := RunSSH(sshUser, node.Spec.IP, keyFile,
			"curl -fsS --max-time 5 http://127.0.0.1:19101/v1/node/device-snapshot")
		g.Expect(result.ExitCode).To(Equal(0),
			"node-agent device snapshot should be reachable on %s\nstdout: %s\nstderr: %s",
			staticNodeName(node), result.Stdout, result.Stderr)

		var snapshot v1.NodeDeviceSnapshot
		g.Expect(json.Unmarshal([]byte(result.Stdout), &snapshot)).To(Succeed(),
			"node-agent device snapshot should be valid JSON on %s: %s", staticNodeName(node), result.Stdout)
		g.Expect(snapshot.Accelerator.Type).NotTo(BeEmpty(),
			"node-agent device snapshot accelerator type should be set on %s", staticNodeName(node))

		if !expectGPU {
			g.Expect(snapshot.Accelerator.Type).To(Equal(v1.StaticNodeAcceleratorTypeCPU),
				"non-GPU node should report CPU accelerator snapshot on %s", staticNodeName(node))
			return
		}

		g.Expect(snapshot.Accelerator.Type).To(Equal(v1.AcceleratorTypeNVIDIAGPU.String()),
			"GPU node should report NVIDIA accelerator snapshot on %s", staticNodeName(node))
		g.Expect(snapshot.Accelerator.Devices).NotTo(BeEmpty(),
			"GPU node should report accelerator devices on %s", staticNodeName(node))

		for _, device := range snapshot.Accelerator.Devices {
			if device.UUID == "" {
				continue
			}
			g.Expect(device.ProductName != "" || device.ProductModel != "").To(BeTrue(),
				"device %s on %s should include product name or model", device.UUID, staticNodeName(node))
			g.Expect(device.MemoryMiB).To(BeNumerically(">", 0),
				"device %s on %s should include memory", device.UUID, staticNodeName(node))
			return
		}

		g.Expect("complete GPU device").To(Equal("present"),
			"GPU node should report at least one device with UUID on %s", staticNodeName(node))
	}, TerminalPhaseTimeout, 5*time.Second).Should(Succeed())
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
			nodeAgent := requireStaticNodeComponent(node, "neutree-node-agent")
			ExpectWithOffset(1, nodeAgent.Args).NotTo(ContainElement(ContainSubstring("--metrics-mode=")))
			ExpectWithOffset(1, nodeAgent.Args).To(ContainElement("--accelerator-type=nvidia_gpu"))
			ExpectWithOffset(1, nodeAgent.Args).To(ContainElement("--accelerator-exporter-port=19400"))
			ExpectWithOffset(1, nodeAgent.Args).To(ContainElement("--accelerator-exporter-metrics-path=/metrics"))
		}
	}

	head := requireStaticNodeRole(nodes, v1.StaticNodeRoleHead)
	vmagent := requireStaticNodeComponent(head, "vmagent")
	requireStaticNodeComponentRunning(head, "vmagent")

	vmagentConfig := requireStaticNodeComponentConfigFile(vmagent, "/etc/neutree/vmagent/config.yaml")
	ExpectWithOffset(1, vmagentConfig.Content).To(ContainSubstring("job_name: static-node-node-exporter"))
	ExpectWithOffset(1, vmagentConfig.Content).To(ContainSubstring("job_name: static-node-ray"))

	if len(gpuNodeIPs) == 0 {
		ExpectWithOffset(1, vmagentConfig.Content).NotTo(ContainSubstring("job_name: accelerator-exporter-nvidia-gpu"))
		return
	}

	ExpectWithOffset(1, vmagentConfig.Content).To(ContainSubstring("job_name: accelerator-exporter-nvidia-gpu"))

	acceleratorTargets := requireStaticNodeComponentConfigFile(
		vmagent,
		"/etc/neutree/vmagent/file_sd/accelerator-exporter-nvidia-gpu.json",
	)

	for _, ip := range gpuNodeIPs {
		ExpectWithOffset(1, acceleratorTargets.Content).To(ContainSubstring(fmt.Sprintf(`"%s:19400"`, ip)))
	}
}

func staticNodeTargets(nodes []v1.StaticNode, port int) []string {
	targets := make([]string, 0, len(nodes))

	for _, node := range nodes {
		if node.Spec == nil || node.Spec.IP == "" {
			continue
		}

		targets = append(targets, fmt.Sprintf(`"%s:%d"`, node.Spec.IP, port))
	}

	return targets
}

// engineContainerIdentity is what identifies one running engine container across
// two observations. Both halves are needed and they catch different things: a
// replaced container comes back with a new ID, and a restarted one keeps its ID
// but gets a new StartedAt. Comparing only IDs would miss the restart.
type engineContainerIdentity struct {
	Node      string `json:"node"`
	Name      string `json:"name"`
	ID        string `json:"id"`
	Image     string `json:"image"`
	StartedAt string `json:"started_at"`
}

// engineContainersOnNodes returns the containers on the cluster's nodes that are
// running one endpoint's engine, keyed by container ID.
//
// Neither of the two things it filters on is a literal in this file, and that is
// the point.
//
// `image` comes from the live Ray Serve config. Filtering on a tag written down
// by the test author is how a comparison ends up being made between two empty
// sets: get the tag wrong and the filter matches nothing, both observations come
// back empty, and "nothing changed" reads as a pass. Letting the system under
// test name its own engine image means the filter cannot drift away from what is
// actually running.
//
// `registryMountPath` is what makes the result one endpoint's containers rather
// than every container on the node built from that image. The image alone is not
// a scope: a cluster routinely runs several endpoints off the same engine image
// — the sibling-isolation and multi-version blocks in the SSH endpoint suite each
// stand up two — and set equality over all of them turns unrelated container
// churn into a failure that reads as "changing an alias disturbed an endpoint".
// The NFS registry mount is per endpoint by construction (`/mnt/<workspace>/
// <endpoint>`, ray_orchestrator.go) and it is attached only to the Backend
// container, not to the Controller or app_builder, which get the image with
// nothing but `--rm`. So it selects exactly the replicas of one endpoint.
//
// The remaining way to observe nothing is that the engine is not running at all,
// which is a broken probe rather than a passing test. So this fails the spec on
// an empty result instead of returning one.
func engineContainersOnNodes(nodes []v1.StaticNode, sshUser, keyFile, image,
	registryMountPath string) map[string]engineContainerIdentity {
	GinkgoHelper()

	Expect(nodes).NotTo(BeEmpty(), "no static nodes to probe for engine containers")
	Expect(image).NotTo(BeEmpty(), "engine image to probe for must not be empty")
	Expect(registryMountPath).NotTo(BeEmpty(), "engine registry mount path to probe for must not be empty")

	// StartedAt has to come from `docker inspect`: `docker ps` only offers
	// CreatedAt, which a restart does not move — so a restarted container would
	// present the same ID and the same CreatedAt and read as undisturbed.
	// `xargs -r` is what keeps an empty container list from invoking
	// `docker inspect` with no arguments, which exits non-zero.
	command := "docker ps -q --no-trunc --filter " + shellSingleQuote("ancestor="+image) +
		" | xargs -r docker inspect --format " +
		shellSingleQuote("{{.Name}}|{{.Id}}|{{.Config.Image}}|{{.State.StartedAt}}|"+
			"{{range .Mounts}}{{.Destination}};{{end}}")

	found := map[string]engineContainerIdentity{}
	skipped := 0

	for _, node := range nodes {
		if node.Spec == nil || node.Spec.IP == "" {
			continue
		}

		r := RunSSH(sshUser, node.Spec.IP, keyFile, command)
		Expect(r.ExitCode).To(Equal(0),
			"engine container probe failed on node %s: %s", node.Spec.IP, r.String())

		for _, line := range strings.Split(strings.TrimSpace(r.Stdout), "\n") {
			fields := strings.Split(strings.TrimSpace(line), "|")
			if len(fields) != 5 {
				continue
			}

			if !slices.Contains(strings.Split(fields[4], ";"), registryMountPath) {
				skipped++

				continue
			}

			found[fields[1]] = engineContainerIdentity{
				Node:      node.Spec.IP,
				Name:      strings.TrimPrefix(fields[0], "/"),
				ID:        fields[1],
				Image:     fields[2],
				StartedAt: fields[3],
			}
		}
	}

	// The gate. Everything downstream compares this map against a later one, and
	// comparing two empty maps succeeds for the wrong reason. The skipped count is
	// in the message because "the image matched nothing" and "the image matched
	// other endpoints' containers but none of this one's" are different failures.
	Expect(found).NotTo(BeEmpty(),
		"engine container probe found no container running image %q with %s mounted, on any of the %d "+
			"cluster nodes (%d container(s) matched the image but not the mount) — the probe is not "+
			"observing this endpoint's engine, so a comparison against it would pass vacuously",
		image, registryMountPath, len(nodes), skipped)

	return found
}

// shellSingleQuote quotes s for a POSIX shell. Image references carry ':' and
// '/', and a docker filter value reaches the node through `ssh <host> <command>`,
// which is a shell.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
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
