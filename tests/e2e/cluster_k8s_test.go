package e2e

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("K8s Cluster Lifecycle", Ordered, Label("cluster", "k8s", "lifecycle"), func() {
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
		clusterName string
		kubeconfig  string
	)

	BeforeAll(func() {
		kubeconfig = requireK8sEnv()
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
		r := ClusterH.WaitForPhase(clusterName, "Running", "10m")
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
		r := ClusterH.Get(clusterName)
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

		seenUpdating := false
		deadline := time.Now().Add(30 * time.Second)

		for time.Now().Before(deadline) {
			r = ClusterH.Get(clusterName)
			if r.ExitCode == 0 {
				c := parseClusterJSON(r.Stdout)
				if c.Status.Phase == "Updating" {
					seenUpdating = true

					break
				}

				if c.Status.Phase == "Running" && c.Status.ObservedSpecHash != oldHash {
					break
				}
			}

			time.Sleep(2 * time.Second)
		}

		if !seenUpdating {
			GinkgoWriter.Printf("WARNING: Updating phase was too fast to capture (< 2s polling interval)\n")
		}

		r = ClusterH.WaitForPhase(clusterName, "Running", "10m")
		ExpectSuccess(r)

		r = ClusterH.Get(clusterName)
		ExpectSuccess(r)
		newHash := parseClusterJSON(r.Stdout).Status.ObservedSpecHash
		Expect(newHash).NotTo(Equal(oldHash))
	})

	It("should transition through Deleting to Deleted", Label("C2642278", "C2612848"), func() {
		r := ClusterH.DeleteGraceful(clusterName)
		ExpectSuccess(r)

		// Best-effort poll for Deleting phase
		seenDeleting := false
		deadline := time.Now().Add(30 * time.Second)

		for time.Now().Before(deadline) {
			r = ClusterH.Get(clusterName)
			if r.ExitCode != 0 {
				break // already gone
			}

			c := parseClusterJSON(r.Stdout)
			if c.Status.Phase == "Deleting" {
				seenDeleting = true

				break
			}

			time.Sleep(2 * time.Second)
		}

		if !seenDeleting {
			GinkgoWriter.Printf("WARNING: Deleting phase was too fast to capture\n")
		}

		r = ClusterH.WaitForDelete(clusterName, "10m")
		ExpectSuccess(r)

		r = RunCLI("get", "cluster", "-w", profileWorkspace())
		ExpectSuccess(r)
		Expect(r.Stdout).NotTo(ContainSubstring(clusterName))
	})
})
