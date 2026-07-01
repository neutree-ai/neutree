package cluster

import (
	"context"
	"fmt"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
)

type StaticNodeClusterDashboardVerifier struct{}

func (StaticNodeClusterDashboardVerifier) VerifyRayCluster(
	_ context.Context,
	cluster *v1.StaticNodeCluster,
) error {
	rayNodes, err := dashboard.NewDashboardService(staticNodeClusterDashboardURL(cluster)).ListNodes()
	if err != nil {
		return errors.Wrap(err, "failed to list Ray nodes")
	}

	return ValidateStaticNodeClusterRayNodes(cluster, rayNodes)
}

func ValidateStaticNodeClusterRayNodes(
	cluster *v1.StaticNodeCluster,
	rayNodes []v1.NodeSummary,
) error {
	if cluster == nil || cluster.Spec == nil {
		return errors.New("static node cluster spec is required")
	}

	aliveByIP := map[string]v1.NodeSummary{}
	for _, node := range rayNodes {
		if node.Raylet.State != v1.AliveNodeState {
			continue
		}

		aliveByIP[node.IP] = node
	}

	for _, nodeSpec := range cluster.Spec.Nodes {
		rayNode, ok := aliveByIP[nodeSpec.IP]
		if !ok {
			return errors.Errorf("Ray node %s is not alive", nodeSpec.IP)
		}

		expectedLabels := rayNodeLabels(cluster, normalizeStaticNodeRole(nodeSpec.Role))
		for key, expected := range expectedLabels {
			if rayNode.Raylet.Labels[key] != expected {
				return errors.Errorf("Ray node %s label %s is %q, want %q", nodeSpec.IP, key, rayNode.Raylet.Labels[key], expected)
			}
		}
	}

	return nil
}

func staticNodeClusterDashboardURL(cluster *v1.StaticNodeCluster) string {
	return fmt.Sprintf("http://%s:%d", staticNodeClusterHeadIP(cluster), defaultRayDashboardPort)
}
