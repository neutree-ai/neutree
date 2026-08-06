package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// This suite is profile-gated because it needs a static NVIDIA cluster whose
// cluster image contains serve/native_engine and a vLLM 0.24.0-compatible
// model. It verifies the full Ray-to-raw-Docker path without a wrapper image.
var _ = Describe("SSH Native vLLM Endpoint", Ordered, Label("endpoint", "ssh", "native-vllm", "vllm-0.24.0"), func() {
	var clusterName string
	var endpointName string

	BeforeAll(func() {
		if profileEngineVersionFor("vllm") != "v0.24.0" {
			Skip("native vLLM demo requires engines.vllm.version=v0.24.0 in the E2E profile")
		}
		if profileModelName() == "" {
			Skip("native vLLM demo requires a configured model")
		}
		clusterName = setupSSHCluster("e2e-native-vllm-")
		SetupModelRegistry()
		endpointName = "e2e-native-vllm-" + Cfg.RunID
	})

	AfterAll(func() {
		if endpointName != "" {
			deleteEndpoint(endpointName)
		}
		TeardownModelRegistry()
		if clusterName != "" {
			teardownCluster(clusterName)
		}
	})

	It("runs the raw vLLM image through the cluster-native runner", func() {
		yamlPath := applyEndpoint(endpointName, clusterName, withEngine("vllm", "v0.24.0"))
		defer os.Remove(yamlPath)
		waitEndpointRunning(endpointName)

		cluster := getClusterFullJSON(clusterName)
		rayHelper := NewRayHelper(cluster.Status.DashboardURL)
		applicationName := profileWorkspace() + "_" + endpointName
		appConfig, err := rayHelper.GetApplicationConfig(applicationName)
		Expect(err).NotTo(HaveOccurred())
		Expect(appConfig).NotTo(BeNil())
		Expect(appConfig.ImportPath).To(Equal("serve.native_engine.app:app_builder"))
		Expect(appConfig.RuntimeEnv).NotTo(HaveKey("container"))
		Expect(appConfig.Args).To(HaveKey("native_container"))

		deployments, err := rayHelper.GetAppRuntimeDeployments(profileWorkspace(), endpointName)
		Expect(err).NotTo(HaveOccurred())
		runner, ok := deployments["NativeEngineRunner"]
		Expect(ok).To(BeTrue(), "native runner deployment should be present")
		Expect(runner.DeploymentConfig.RayActorOptions.NumGPUs).To(BeNumerically(">", 0))

		ep := getEndpoint(endpointName)
		code, body, err := inferChat(ep.Status.ServiceURL, "Say hello in five words.")
		Expect(err).NotTo(HaveOccurred())
		Expect(code).To(Equal(http.StatusOK), "native vLLM inference failed: %s", body)
		Expect(body).To(ContainSubstring("choices"))
	})

	It("relays native logs and metrics through Ray", func() {
		Eventually(func(g Gomega) {
			g.Expect(string(getEndpointLogSources(endpointName))).To(ContainSubstring("NativeEngineRunner"))
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		Eventually(func(g Gomega) {
			g.Expect(nativeRunnerLogs(endpointName)).To(ContainSubstring("[native-engine:"))
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		Eventually(func(g Gomega) {
			g.Expect(staticRayMetrics(clusterName)).To(ContainSubstring("ray_vllm_"))
		}, 2*time.Minute, 5*time.Second).Should(Succeed())
	})
})

func nativeRunnerLogs(endpointName string) string {
	body := getEndpointLogSources(endpointName)
	var sources logSourcesResponse
	ExpectWithOffset(1, json.Unmarshal(body, &sources)).To(Succeed())
	for _, deployment := range sources.Deployments {
		if deployment.Name != "NativeEngineRunner" {
			continue
		}
		for _, replica := range deployment.Replicas {
			if replica.ReplicaID != "" {
				return getEndpointReplicaLog(endpointName, replica.ReplicaID, "stdout", 1000)
			}
		}
	}
	return ""
}

func staticRayMetrics(clusterName string) string {
	nodes := getStaticNodesForCluster(clusterName)
	sshUser := profileSSHUser()
	if sshUser == "" {
		sshUser = defaultSSHUser
	}
	keyFile := expandHome(profile.SSHNodes[0].KeyFile)
	for _, node := range nodes {
		if node.Spec == nil || node.Status == nil || node.Status.Accelerator == nil ||
			node.Status.Accelerator.Type != v1.AcceleratorTypeNVIDIAGPU.String() {
			continue
		}
		command := fmt.Sprintf(
			"container=$(%s); test -n \"$container\" && docker exec \"$container\" curl -fsS http://127.0.0.1:54311/metrics",
			rayContainerNameCommand(clusterName, node.Spec.Role),
		)
		result := RunSSH(sshUser, node.Spec.IP, keyFile, command)
		if result.ExitCode == 0 && strings.Contains(result.Stdout, "ray_vllm_") {
			return result.Stdout
		}
	}
	return ""
}
