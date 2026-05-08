package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// NEU-423 / TestRail C2649684:
// On an SSH/Ray cluster, when a Serve replica's actor fails to initialize
// Ray Serve removes it from the live applications response. Verify that
// /log-sources still surfaces the failed actor (with failed=true) via the
// state API fallback, and /logs/<replica_id>/stderr streams the actor's
// stderr containing the Python traceback.
var _ = Describe("SSH Endpoint Failure Logs", Ordered, Label("endpoint", "ssh", "logs", "failure", "C2649684"), func() {
	var clusterName string

	BeforeAll(func() {
		if profileModelName() == "" {
			Skip("Model name not configured in profile, skipping SSH endpoint failure-log tests")
		}

		clusterName = setupSSHCluster("e2e-ep-ssh-fail-")

		By("Setting up model registry")
		SetupModelRegistry()
	})

	AfterAll(func() {
		TeardownModelRegistry()
		teardownCluster(clusterName)
	})

	It("should expose failed actor stderr after init crash", func() {
		epName := "e2e-ep-ssh-faillog-" + Cfg.RunID
		DeferCleanup(func() { deleteEndpoint(epName) })

		By("Applying endpoint with non-existent model to force init failure")
		yamlPath := applyEndpoint(epName, clusterName,
			withModel("non-existent-model-"+Cfg.RunID, "v0.0.0"),
			withoutForceUpdate(),
		)
		defer os.Remove(yamlPath)

		By("Waiting for endpoint to reach Failed phase")
		waitEndpointFailed(epName)

		By("Calling GET /log-sources and expecting at least one failed replica")
		var sources logSourcesResponse
		Eventually(func(g Gomega) {
			body := getEndpointLogSources(epName)
			g.Expect(json.Unmarshal(body, &sources)).To(Succeed(), "log-sources body: %s", string(body))

			var foundFailed bool
			for _, dep := range sources.Deployments {
				for _, r := range dep.Replicas {
					if r.Failed && r.ReplicaID != "" {
						foundFailed = true
						break
					}
				}
				if foundFailed {
					break
				}
			}
			g.Expect(foundFailed).To(BeTrue(),
				"expected at least one Replica with failed=true; raw response: %s", string(body))
		}, 2*time.Minute, 10*time.Second).Should(Succeed())

		By("Picking the failed replica_id and streaming its stderr")
		var failedReplicaID string
		for _, dep := range sources.Deployments {
			for _, r := range dep.Replicas {
				if r.Failed {
					failedReplicaID = r.ReplicaID
					break
				}
			}
			if failedReplicaID != "" {
				break
			}
		}
		Expect(failedReplicaID).NotTo(BeEmpty())

		body := getEndpointReplicaLog(epName, failedReplicaID, "stderr", 500)
		Expect(body).NotTo(BeEmpty(), "stderr body should not be empty for failed replica")
		Expect(strings.ToLower(body)).To(SatisfyAny(
			ContainSubstring("traceback"),
			ContainSubstring("error"),
		), "stderr should surface a Python traceback / error indicator from the failed actor")
	})
})

// ===== test-local helpers (HTTP calls to neutree-api) =====

type logSourcesResponse struct {
	Deployments []struct {
		Name     string `json:"name"`
		Replicas []struct {
			ReplicaID string `json:"replica_id"`
			Failed    bool   `json:"failed,omitempty"`
		} `json:"replicas"`
	} `json:"deployments"`
}

func neutreeAPIRequest(method, path string) ([]byte, int) {
	GinkgoHelper()

	url := strings.TrimRight(Cfg.ServerURL, "/") + path
	req, err := http.NewRequest(method, url, nil)
	Expect(err).NotTo(HaveOccurred())

	authValue := Cfg.APIKey
	if !strings.HasPrefix(authValue, "Bearer ") {
		authValue = "Bearer " + authValue
	}
	req.Header.Set("Authorization", authValue)

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	Expect(err).NotTo(HaveOccurred())
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	Expect(err).NotTo(HaveOccurred())

	return body, resp.StatusCode
}

func getEndpointLogSources(epName string) []byte {
	GinkgoHelper()
	path := fmt.Sprintf("/api/v1/endpoints/%s/%s/log-sources", profileWorkspace(), epName)
	body, code := neutreeAPIRequest(http.MethodGet, path)
	Expect(code).To(Equal(http.StatusOK), "log-sources call failed: %s", string(body))
	return body
}

func getEndpointReplicaLog(epName, replicaID, logType string, lines int) string {
	GinkgoHelper()
	path := fmt.Sprintf("/api/v1/endpoints/%s/%s/logs/%s/%s?lines=%d",
		profileWorkspace(), epName, replicaID, logType, lines)
	body, code := neutreeAPIRequest(http.MethodGet, path)
	Expect(code).To(Equal(http.StatusOK), "log stream call failed: %s", string(body))
	return string(body)
}
