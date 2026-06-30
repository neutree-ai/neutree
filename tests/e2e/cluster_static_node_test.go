package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/semver"
)

var _ = Describe("Static Node Cluster Lifecycle", Ordered, Label("cluster", "ssh", "static-node", "lifecycle"), func() {
	var ClusterH *ClusterHelper
	var imageRegistryReady bool

	BeforeAll(func() {
		requireImageRegistryProfile()
		requireStaticNodeClusterProfile()

		By("Setting up image registry")
		SetupImageRegistry()
		imageRegistryReady = true
		ClusterH = NewClusterHelper()
	})

	var (
		clusterName   string
		legacyName    string
		headIP        string
		workerIPs     []string
		sshUser       string
		sshPrivateKey string
	)

	BeforeAll(func() {
		headIP, workerIPs, sshUser, sshPrivateKey = requireSSHProfile()
		clusterName = "e2e-static-ssh-" + Cfg.RunID
		legacyName = "e2e-static-upg-" + Cfg.RunID
	})

	AfterAll(func() {
		if ClusterH != nil && clusterName != "" {
			ClusterH.EnsureDeleted(clusterName)
		}
		if ClusterH != nil && legacyName != "" {
			ClusterH.EnsureDeleted(legacyName)
		}
		if imageRegistryReady {
			TeardownImageRegistry()
		}
	})

	It("should reject direct StaticNodeCluster writes", Label("read-only"), func() {
		yaml := writeTempE2EYAML("static-node-cluster-*.yaml", fmt.Sprintf(`apiVersion: v1
kind: StaticNodeCluster
metadata:
  name: direct-static-%s
  workspace: %s
spec:
  version: %s
`, Cfg.RunID, profileWorkspace(), profileClusterVersion()))
		defer os.Remove(yaml)

		r := RunCLI("apply", "-f", yaml, "--force-update")
		Expect(r.ExitCode).NotTo(Equal(0), "direct StaticNodeCluster apply should be rejected")
	})

	It("should create a new-version SSH cluster and derive static resources", Label("create"), func() {
		yaml := renderSSHClusterYAML(map[string]any{
			"name":            clusterName,
			"version":         profileClusterVersion(),
			"head_ip":         headIP,
			"worker_ips":      workerIPs,
			"ssh_user":        sshUser,
			"ssh_private_key": sshPrivateKey,
		})

		r := ClusterH.Apply(yaml)
		ExpectSuccess(r)

		r = ClusterH.WaitForPhase(clusterName, v1.ClusterPhaseRunning, TerminalPhaseTimeout)
		ExpectSuccess(r)
		eventuallyStaticNodeClusterReady(clusterName, 1+len(workerIPs), TerminalPhaseTimeout)
	})

	It("should update worker list only after stale nodes are cleaned", Label("update"), func() {
		r := ClusterH.Get(clusterName)
		ExpectSuccess(r)
		oldHash := parseClusterJSON(r.Stdout).Status.ObservedSpecHash

		yaml := renderSSHClusterYAML(map[string]any{
			"name":            clusterName,
			"version":         profileClusterVersion(),
			"head_ip":         headIP,
			"ssh_user":        sshUser,
			"ssh_private_key": sshPrivateKey,
		})

		r = ClusterH.Apply(yaml)
		ExpectSuccess(r)
		ClusterH.EventuallyInPhase(clusterName, v1.ClusterPhaseUpdating, "", IntermediatePhaseTimeout)

		r = ClusterH.WaitForPhase(clusterName, v1.ClusterPhaseRunning, TerminalPhaseTimeout)
		ExpectSuccess(r)
		ClusterH.EventuallyObservedSpecHashAdvanced(clusterName, oldHash, IntermediatePhaseTimeout)
		eventuallyStaticNodeClusterReady(clusterName, 1, TerminalPhaseTimeout)
		assertStaticNodesForCluster(clusterName, []string{headIP})
	})

	It("should delete static resources with cluster deletion", Label("delete"), func() {
		r := ClusterH.DeleteAsync(clusterName)
		ExpectSuccess(r)

		ClusterH.EventuallyInPhase(clusterName, v1.ClusterPhaseDeleting, "", IntermediatePhaseTimeout)
		ClusterH.EventuallyInPhase(clusterName, v1.ClusterPhaseDeleted, "", TerminalPhaseTimeout)

		r = ClusterH.WaitForDelete(clusterName, TerminalPhaseTimeout)
		ExpectSuccess(r)
		assertNoStaticNodeCluster(clusterName)
		assertStaticNodesForCluster(clusterName, nil)
	})

	It("should keep legacy version on legacy flow then recreate into static flow on upgrade", Label("upgrade"), func() {
		yaml := renderSSHClusterYAML(map[string]any{
			"name":            legacyName,
			"version":         profile.Cluster.OldVersion,
			"head_ip":         headIP,
			"worker_ips":      workerIPs,
			"ssh_user":        sshUser,
			"ssh_private_key": sshPrivateKey,
		})

		r := ClusterH.Apply(yaml)
		ExpectSuccess(r)
		r = ClusterH.WaitForPhase(legacyName, v1.ClusterPhaseRunning, TerminalPhaseTimeout)
		ExpectSuccess(r)
		assertNoStaticNodeCluster(legacyName)

		yaml = renderSSHClusterYAML(map[string]any{
			"name":            legacyName,
			"version":         profileClusterVersion(),
			"head_ip":         headIP,
			"worker_ips":      workerIPs,
			"ssh_user":        sshUser,
			"ssh_private_key": sshPrivateKey,
		})

		r = ClusterH.Apply(yaml)
		ExpectSuccess(r)
		r = ClusterH.WaitForPhase(legacyName, v1.ClusterPhaseRunning, TerminalPhaseTimeout)
		ExpectSuccess(r)
		eventuallyStaticNodeClusterReady(legacyName, 1+len(workerIPs), TerminalPhaseTimeout)
	})
})

func requireStaticNodeClusterProfile() {
	newVersion := profileClusterVersion()
	enabled, err := semver.LessThan("v1.0.1", newVersion)
	Expect(err).NotTo(HaveOccurred(), "cluster.version must be semver")
	if !enabled {
		Skip("cluster.version must be > v1.0.1 for static node cluster tests")
	}

	if profile.Cluster.OldVersion == "" {
		Skip("cluster.old_version not configured, skipping static node legacy upgrade tests")
	}

	oldIsNew, err := semver.LessThan("v1.0.1", profile.Cluster.OldVersion)
	Expect(err).NotTo(HaveOccurred(), "cluster.old_version must be semver")
	if oldIsNew {
		Skip("cluster.old_version must be <= v1.0.1 for static node legacy gate tests")
	}

	if profileSSHHeadIP() == "" {
		Skip("SSH head IP not configured, skipping static node cluster tests")
	}

	if profileSSHWorkerIPs() == "" {
		Skip("at least one SSH worker IP is required for static node full lifecycle tests")
	}
}

func assertStaticNodeClusterReady(name string, desiredNodes int) {
	r := RunCLI("get", "StaticNodeCluster", name, "-w", profileWorkspace(), "-o", "json")
	ExpectSuccess(r)

	var cluster v1.StaticNodeCluster
	ExpectWithOffset(1, json.Unmarshal([]byte(r.Stdout), &cluster)).To(Succeed())
	Expect(cluster.Status).NotTo(BeNil())
	Expect(cluster.Status.Phase).To(Equal(v1.StaticNodeClusterPhaseReady))
	Expect(cluster.Status.DesiredNodes).To(Equal(desiredNodes))
	Expect(cluster.Status.ReadyNodes).To(Equal(desiredNodes))
	Expect(cluster.Status.HeadReady).To(BeTrue())
	Expect(cluster.Status.Version).To(Equal(profileClusterVersion()))
}

func eventuallyStaticNodeClusterReady(name string, desiredNodes int, timeout time.Duration) {
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
		g.Expect(cluster.Status.Version).To(Equal(profileClusterVersion()))
	}, timeout, 5*time.Second).Should(Succeed())
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

func writeTempE2EYAML(pattern string, content string) string {
	file, err := os.CreateTemp("", pattern)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	defer file.Close()

	_, err = file.WriteString(content)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())

	return file.Name()
}
