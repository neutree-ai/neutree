package e2e

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

var _ = Describe("K8s Cluster Lifecycle", Ordered, Label("cluster", "k8s", "lifecycle"), func() {
	var ClusterH *ClusterHelper

	BeforeAll(func() {
		requireImageRegistryProfile()

		By("Setting up image registry")
		SetupImageRegistry()
		ClusterH = NewClusterHelper()
	})

	AfterAll(func() {
		TeardownImageRegistry()
	})

	var (
		clusterName string
		kubeconfig  string
	)

	BeforeAll(func() {
		kubeconfig = requireK8sProfile()
		clusterName = "e2e-k8s-" + Cfg.RunID

		yaml := renderK8sClusterYAML(map[string]string{
			"name":       clusterName,
			"kubeconfig": kubeconfig,
		})

		r := ClusterH.Apply(yaml)
		ExpectSuccess(r)
	})

	AfterAll(func() {
		ClusterH.EnsureDeleted(clusterName)
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

		yaml := renderK8sClusterYAML(map[string]string{
			"name":            clusterName,
			"kubeconfig":      kubeconfig,
			"image_registry":  testImageRegistry(),
			"router_replicas": "2",
		})
		r = ClusterH.Apply(yaml)
		ExpectSuccess(r)

		By("Waiting for Updating phase")
		ClusterH.EventuallyInPhase(clusterName, v1.ClusterPhaseUpdating, "", IntermediatePhaseTimeout)

		By("Waiting for Running phase")
		r = ClusterH.WaitForPhase(clusterName, v1.ClusterPhaseRunning, TerminalPhaseTimeout)
		ExpectSuccess(r)

		By("Verifying observedSpecHash advanced after update")
		r = ClusterH.Get(clusterName)
		ExpectSuccess(r)
		Expect(parseClusterJSON(r.Stdout).Status.ObservedSpecHash).NotTo(Equal(oldHash),
			"observedSpecHash should change after a successful update")
	})

	It("should transition through Deleting to Deleted", Label("C2642278", "C2612848"), func() {
		r := ClusterH.WaitForPhase(clusterName, v1.ClusterPhaseRunning, TerminalPhaseTimeout)
		ExpectSuccess(r)

		r = ClusterH.DeleteAsync(clusterName)
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
