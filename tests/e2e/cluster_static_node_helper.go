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
	r := RunCLI("get", "StaticNode", "-w", profileWorkspace(), "-o", "json")
	if len(expectedIPs) == 0 {
		if r.ExitCode != 0 || strings.Contains(r.Stdout, "No staticnode resources found") {
			return
		}
	} else {
		ExpectSuccess(r)
	}

	nodes := parseStaticNodeList(r.Stdout)

	actual := map[string]struct{}{}

	for _, node := range nodes {
		if node.Spec == nil || node.Spec.Cluster != clusterName {
			continue
		}

		actual[node.Spec.IP] = struct{}{}
	}

	Expect(actual).To(HaveLen(len(expectedIPs)))

	for _, ip := range expectedIPs {
		Expect(actual).To(HaveKey(ip))
	}
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
