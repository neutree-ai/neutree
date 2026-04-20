package e2e

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

var _ = Describe("SSH Cluster Lifecycle", Ordered, Label("cluster", "ssh", "lifecycle"), func() {
	var ClusterH *ClusterHelper

	BeforeAll(func() {
		requireImageRegistryEnv()

		By("Setting up image registry")
		SetupImageRegistry()
		ClusterH = NewClusterHelper()
	})

	AfterAll(func() {
		TeardownImageRegistry()
	})

	var (
		clusterName   string
		headIP        string
		workerIPs     string
		sshUser       string
		sshPrivateKey string
	)

	BeforeAll(func() {
		headIP, workerIPs, sshUser, sshPrivateKey = requireSSHEnv()
		clusterName = "e2e-ssh-" + Cfg.RunID

		yaml := renderSSHClusterYAML(map[string]string{
			"name":            clusterName,
			"head_ip":         headIP,
			"worker_ips":      workerIPs,
			"ssh_user":        sshUser,
			"ssh_private_key": sshPrivateKey,
		})

		By("Applying SSH cluster")
		r := ClusterH.Apply(yaml)
		ExpectSuccess(r)
	})

	AfterAll(func() {
		ClusterH.EnsureDeleted(clusterName)
	})

	It("should show Initializing immediately after creation", Label("C2612656"), func() {
		ClusterH.EventuallyInPhase(clusterName, v1.ClusterPhaseInitializing, "", IntermediatePhaseTimeout)
	})

	It("should transition to Running", Label("C2613101"), func() {
		r := ClusterH.WaitForPhase(clusterName, v1.ClusterPhaseRunning, TerminalPhaseTimeout)
		ExpectSuccess(r)

		r = ClusterH.Get(clusterName)
		ExpectSuccess(r)
		c := parseClusterJSON(r.Stdout)

		Expect(c.Status.Phase).To(BeEquivalentTo("Running"))
		Expect(c.Status.Initialized).To(BeTrue())
		Expect(c.Status.ObservedSpecHash).NotTo(BeEmpty())
		Expect(c.Status.ReadyNodes).To(BeNumerically(">=", c.Status.DesiredNodes))

		expectedNodes := 1
		if workerIPs != "" {
			expectedNodes += len(strings.Split(workerIPs, ","))
		}

		Expect(c.Status.DesiredNodes).To(Equal(expectedNodes))
		Expect(c.Status.DashboardURL).NotTo(BeEmpty())
		Expect(c.Status.ErrorMessage).To(BeEmpty())
		Expect(c.Status.ResourceInfo).NotTo(BeNil())
	})

	It("should show Updating then Running on spec change", Label("C2642277"), func() {
		// Safety net for --test-filter runs that skip the preceding "transition
		// to Running" case: ensure the cluster is Running before reading Status.
		r := ClusterH.WaitForPhase(clusterName, v1.ClusterPhaseRunning, TerminalPhaseTimeout)
		ExpectSuccess(r)

		r = ClusterH.Get(clusterName)
		ExpectSuccess(r)
		oldHash := parseClusterJSON(r.Stdout).Status.ObservedSpecHash

		yaml := renderSSHClusterYAML(map[string]string{
			"name":             clusterName,
			"head_ip":          headIP,
			"worker_ips":       workerIPs,
			"ssh_user":         sshUser,
			"ssh_private_key":  sshPrivateKey,
			"accelerator_type": "cpu",
		})
		r = ClusterH.Apply(yaml)
		ExpectSuccess(r)

		By("Waiting for Updating phase and spec change")
		ClusterH.WaitForClusterUpdating(clusterName, oldHash, IntermediatePhaseTimeout)

		By("Waiting for Running phase")
		r = ClusterH.WaitForPhase(clusterName, v1.ClusterPhaseRunning, TerminalPhaseTimeout)
		ExpectSuccess(r)
	})

	It("should transition through Deleting to Deleted", Label("C2642278", "C2612848", "C2612847"), func() {
		r := ClusterH.WaitForPhase(clusterName, v1.ClusterPhaseRunning, TerminalPhaseTimeout)
		ExpectSuccess(r)

		r = ClusterH.DeleteGraceful(clusterName)
		ExpectSuccess(r)

		By("Waiting for Deleting phase")
		ClusterH.EventuallyInPhase(clusterName, v1.ClusterPhaseDeleting, "", IntermediatePhaseTimeout)

		By("Waiting for Deleted phase")
		ClusterH.EventuallyInPhase(clusterName, v1.ClusterPhaseDeleted, "", TerminalPhaseTimeout)

		By("Waiting for full removal from list")
		r = ClusterH.WaitForDelete(clusterName, TerminalPhaseTimeout)
		ExpectSuccess(r)

		r = RunCLI("get", "cluster", "-w", profileWorkspace())
		ExpectSuccess(r)
		Expect(r.Stdout).NotTo(ContainSubstring(clusterName))
	})
})
