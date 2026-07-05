package e2e

import (
	"context"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
)

func assertK8sNodeAgentEndpointGPUMetrics(clusterName, endpointName string) {
	ctx := context.Background()
	k8sH := NewK8sHelper(profileKubeconfig())
	namespace := k8sClusterNamespace(clusterName)

	metrics := eventuallyNodeAgentMetricsContain(ctx, k8sH, namespace,
		fmt.Sprintf(`endpoint="%s"`, endpointName),
		"neutree_endpoint_replica_gpu_allocation",
		"neutree_endpoint_replica_gpu_memory_used_bytes",
		"neutree_endpoint_replica_gpu_utilization_ratio",
	)
	ExpectWithOffset(1, metrics).NotTo(ContainSubstring("vdevice"))
}

func k8sClusterNamespace(clusterName string) string {
	cluster := getClusterFullJSON(clusterName)
	ExpectWithOffset(1, cluster.Metadata).NotTo(BeNil())

	return ClusterNamespace(cluster.Metadata.Workspace, cluster.Metadata.Name, cluster.ID)
}

func eventuallyNodeAgentMetricsContain(
	ctx context.Context,
	k8sH *K8sHelper,
	namespace string,
	substrings ...string,
) string {
	var matched string
	var lastBodies []string

	EventuallyWithOffset(1, func(g Gomega) {
		pods, err := k8sH.ListPods(ctx, namespace, "app=neutree-node-agent")
		g.Expect(err).NotTo(HaveOccurred(), "should list neutree-node-agent pods")
		g.Expect(pods).NotTo(BeEmpty(), "neutree-node-agent should have pods")

		lastBodies = lastBodies[:0]
		for _, pod := range pods {
			if pod.Status.Phase != corev1.PodRunning {
				continue
			}

			raw, err := k8sH.PodProxyGetRaw(ctx, namespace, pod.Name, "19101", "/metrics")
			if err != nil {
				continue
			}

			body := string(raw)
			lastBodies = append(lastBodies, body)
			if containsAll(body, substrings...) {
				matched = body
				return
			}
		}

		g.Expect(strings.Join(lastBodies, "\n")).To(
			Satisfy(func(body string) bool { return containsAll(body, substrings...) }),
			"node-agent metrics should contain %v",
			substrings,
		)
	}, TerminalPhaseTimeout, 5*time.Second).Should(Succeed())

	return matched
}

func containsAll(value string, substrings ...string) bool {
	for _, substring := range substrings {
		if !strings.Contains(value, substring) {
			return false
		}
	}

	return true
}
