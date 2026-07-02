package cluster

import (
	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

func ListStaticNodesByCluster(
	store storage.Storage,
	workspace string,
	clusterName string,
) ([]*v1.StaticNode, error) {
	items, err := store.ListStaticNode(storage.ListOption{
		Filters: []storage.Filter{
			{Column: "metadata->>workspace", Operator: "eq", Value: workspace},
			{Column: "spec->>cluster", Operator: "eq", Value: clusterName},
		},
	})
	if err != nil {
		return nil, err
	}

	nodes := make([]*v1.StaticNode, 0, len(items))

	for i := range items {
		node := &items[i]
		if node.Spec == nil {
			continue
		}

		if node.Metadata.Workspace != workspace || node.Spec.Cluster != clusterName {
			continue
		}

		nodes = append(nodes, node)
	}

	return nodes, nil
}
