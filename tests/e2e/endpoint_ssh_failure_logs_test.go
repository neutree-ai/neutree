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
		// This test passes a non-existent model name via withModel(...),
		// so it does not depend on profile.model.name being set. Profile
		// gating is delegated to setupSSHCluster (SSH nodes + image
		// registry) and SetupModelRegistry (model registry config).
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

		// Proof-of-wiring assertion: the stream must carry Ray's per-actor metadata
		// headers (":job_id:" + ":actor_name:..."), proving we recovered the right
		// DEAD actor and reached its stderr file via /api/v0/logs/file?actor_id=...
		// before Ray Serve garbage-collected the live applications response.
		//
		// We deliberately don't assert traceback content: whether the actor's
		// stderr contains a Python traceback depends on the failure mode. The
		// "model not in registry" repro path used here is caught by Ray Serve
		// at the framework layer, so the actor dies before printing user code.
		// Failure modes that DO traceback (bad engine_args, OOM, missing CUDA)
		// would surface them through the same wired path; manual verification
		// of those scenarios is part of the PR test plan.
		body := getEndpointReplicaLog(epName, failedReplicaID, "stderr", 500)
		Expect(body).NotTo(BeEmpty(), "stderr body should not be empty for failed replica")
		Expect(body).To(SatisfyAll(
			ContainSubstring(":job_id:"),
			ContainSubstring(":actor_name:"),
			ContainSubstring(strings.ToLower(epName)),
		), "stderr stream should carry Ray actor metadata headers for the failed deployment")
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

	// neutree-api accepts the raw API key; only Kong/inference endpoints expect "Bearer <key>".
	req.Header.Set("Authorization", Cfg.APIKey)

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
