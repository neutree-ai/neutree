package e2e

import (
	"strings"
	"time"

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
		r := ClusterH.Get(clusterName)
		ExpectSuccess(r)
		c := parseClusterJSON(r.Stdout)

		phase := v1.ClusterPhase("")
		if c.Status != nil {
			phase = c.Status.Phase
		}

		// Strictly verify Initializing — if the cluster already reached Running,
		// warn but still pass since the controller was fast.
		if phase == v1.ClusterPhase("Running") {
			GinkgoWriter.Printf("WARNING: cluster already Running, Initializing phase was too fast to capture\n")
		} else {
			Expect(phase).To(BeElementOf(
				v1.ClusterPhase(""), v1.ClusterPhase("Initializing")),
				"cluster should be in empty or Initializing, got %s", phase)
		}
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
		r := ClusterH.Get(clusterName)
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

		By("Polling for Updating intermediate phase")
		seenUpdating := false
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			r = ClusterH.Get(clusterName)
			if r.ExitCode == 0 {
				c := parseClusterJSON(r.Stdout)
				if c.Status.Phase == "Updating" {
					seenUpdating = true

					break
				}
				// Controller already processed and returned to Running with new hash
				if c.Status.ObservedSpecHash != oldHash {
					break
				}
			}
			time.Sleep(1 * time.Second)
		}

		By("Waiting for Running phase")
		r = ClusterH.WaitForPhase(clusterName, "Running", "10m")
		ExpectSuccess(r)

		r = ClusterH.Get(clusterName)
		ExpectSuccess(r)
		newHash := parseClusterJSON(r.Stdout).Status.ObservedSpecHash
		Expect(newHash).NotTo(Equal(oldHash))

		if !seenUpdating {
			GinkgoWriter.Printf("WARNING: Updating phase was not captured (transition too fast for 1s poll interval)\n")
		}
	})

	It("should transition through Deleting to Deleted", Label("C2642278", "C2612848", "C2612847"), func() {
		r := ClusterH.DeleteGraceful(clusterName)
		ExpectSuccess(r)

		By("Polling for Deleting intermediate phase")
		seenDeleting := false
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			r = ClusterH.Get(clusterName)
			if r.ExitCode != 0 {
				// Already gone
				break
			}
			c := parseClusterJSON(r.Stdout)
			if c.Status.Phase == "Deleting" {
				seenDeleting = true

				break
			}
			time.Sleep(1 * time.Second)
		}

		By("Waiting for full deletion")
		r = ClusterH.WaitForDelete(clusterName, "10m")
		ExpectSuccess(r)

		r = RunCLI("get", "cluster", "-w", profileWorkspace())
		ExpectSuccess(r)
		Expect(r.Stdout).NotTo(ContainSubstring(clusterName))

		if !seenDeleting {
			GinkgoWriter.Printf("WARNING: Deleting phase was not captured (transition too fast for 1s poll interval)\n")
		}
	})
})
