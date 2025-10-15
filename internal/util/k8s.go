package util

import v1 "github.com/neutree-ai/neutree/api/v1"

func ClusterNamespace(cluster *v1.Cluster) string {
	return "neutree-cluster-" + HashString(cluster.Key())
}
