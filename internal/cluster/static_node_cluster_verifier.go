package cluster

import (
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func staticNodeClusterDashboardURL(cluster *v1.StaticNodeCluster) string {
	return fmt.Sprintf("http://%s:%d", staticNodeClusterHeadIP(cluster), defaultRayDashboardPort)
}
