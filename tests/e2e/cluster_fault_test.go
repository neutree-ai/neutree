package e2e

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Cluster Fault Recovery", Ordered, Label("fault"), func() {
	var ClusterH *ClusterHelper

	BeforeAll(func() {
		if profile.ImageRegistry.URL == "" {
			Skip("ImageRegistry.URL not configured in profile, skipping fault tests")
		}
		if profile.ImageRegistry.Repository == "" {
			Skip("ImageRegistry.Repository not configured in profile, skipping fault tests")
		}

		By("Setting up image registry")
		SetupImageRegistry()
		ClusterH = NewClusterHelper()
	})

	AfterAll(func() {
		By("Tearing down image registry")
		TeardownImageRegistry()
	})

	// --- SSH Head Node Fault Recovery ---

	Describe("SSH Head Node Recovery", Ordered, Label("ssh"), func() {
		var (
			clusterName   string
			headIP        string
			workerIPs     string
			sshUser       string
			sshPrivateKey string
			sshKeyFile    string
		)

		BeforeAll(func() {
			headIP, workerIPs, sshUser, sshPrivateKey = requireSSHEnv()
			if len(profile.SSHNodes) == 0 || profile.SSHNodes[0].KeyFile == "" {
				Skip("SSH key file path not configured in profile, skipping recovery tests")
			}
			sshKeyFile = expandHome(profile.SSHNodes[0].KeyFile)
			clusterName = "e2e-ssh-fault-" + Cfg.RunID

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

			By("Waiting for Running phase")
			r = ClusterH.WaitForPhase(clusterName, "Running", "10m")
			ExpectSuccess(r)
		})

		AfterAll(func() {
			By("Force-deleting fault test cluster")
			ClusterH.EnsureDeleted(clusterName)
		})

		It("should recover after head raylet is killed", Label("C2614001"), func() {
			By("Killing raylet process inside ray_container (keeping GCS/dashboard alive)")
			r := RunSSH(sshUser, headIP, sshKeyFile,
				"docker exec ray_container pkill -f 'dist-packages/ray/core/src/ray/raylet/raylet' || true")
			ExpectSuccess(r)

			By("Waiting for cluster to recover to Running")
			r = ClusterH.WaitForPhase(clusterName, "Running", "5m")
			ExpectSuccess(r)

			By("Verifying cluster is fully healthy")
			r = ClusterH.Get(clusterName)
			ExpectSuccess(r)
			c := parseClusterJSON(r.Stdout)
			Expect(c.Status.ReadyNodes).To(Equal(c.Status.DesiredNodes))
		})

		It("should recover after all head Ray processes are stopped", Label("C2614002"), func() {
			By("Stopping all Ray processes inside ray_container")
			r := RunSSH(sshUser, headIP, sshKeyFile, "docker exec ray_container ray stop --force || true")
			ExpectSuccess(r)

			By("Waiting for cluster to recover to Running")
			r = ClusterH.WaitForPhase(clusterName, "Running", "5m")
			ExpectSuccess(r)

			By("Verifying cluster is fully healthy")
			r = ClusterH.Get(clusterName)
			ExpectSuccess(r)
			c := parseClusterJSON(r.Stdout)
			Expect(c.Status.ReadyNodes).To(Equal(c.Status.DesiredNodes))
		})
	})
})
