package controllers

import (
	"time"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

const (
	staticNodeClusterKind = "StaticNodeCluster"
	staticNodeKind        = "StaticNode"
	staticNodeListKind    = "StaticNodeList"
)

func listStaticNodes(
	store storage.Storage,
	workspace string,
	clusterName string,
) ([]*v1.StaticNode, error) {
	if store == nil {
		return nil, errors.New("storage is required")
	}

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
		node := items[i]
		if node.Spec == nil {
			continue
		}

		if node.Metadata.Workspace != workspace || node.Spec.Cluster != clusterName {
			continue
		}

		nodes = append(nodes, &node)
	}

	return nodes, nil
}

func upsertStaticNode(store storage.Storage, node *v1.StaticNode) error {
	if store == nil {
		return errors.New("storage is required")
	}

	if node == nil {
		return errors.New("static node is required")
	}

	prepareStaticNode(node)

	existing, found, err := findStaticNode(store, node.Metadata.Workspace, node.Metadata.Name)
	if err != nil {
		return err
	}

	if !found {
		return store.CreateStaticNode(node)
	}

	if err := validateStaticNodeOwner(existing, node); err != nil {
		return err
	}

	node.ID = existing.ID

	return store.UpdateStaticNode(existing.GetID(), node)
}

func validateStaticNodeOwner(existing *v1.StaticNode, desired *v1.StaticNode) error {
	if existing == nil || desired == nil || existing.Spec == nil || desired.Spec == nil {
		return nil
	}

	if existing.Spec.Cluster == "" || desired.Spec.Cluster == "" || existing.Spec.Cluster == desired.Spec.Cluster {
		return nil
	}

	name := ""
	if desired.Metadata != nil {
		name = desired.Metadata.Name
	}

	return errors.Errorf("static node %s is already owned by static node cluster %s", name, existing.Spec.Cluster)
}

func softDeleteStaticNode(store storage.Storage, node *v1.StaticNode) error {
	if store == nil {
		return errors.New("storage is required")
	}

	if node == nil {
		return errors.New("static node is required")
	}

	prepareStaticNode(node)

	if node.Metadata.DeletionTimestamp == "" {
		node.Metadata.DeletionTimestamp = time.Now().UTC().Format(time.RFC3339)
	}

	return store.UpdateStaticNode(node.GetID(), node)
}

func hardDeleteStaticNode(store storage.Storage, node *v1.StaticNode) error {
	if store == nil {
		return errors.New("storage is required")
	}

	if node == nil {
		return errors.New("static node is required")
	}

	prepareStaticNode(node)

	return store.DeleteStaticNode(node.GetID())
}

func hardDeleteStaticNodeCluster(store storage.Storage, cluster *v1.StaticNodeCluster) error {
	if store == nil {
		return errors.New("storage is required")
	}

	if cluster == nil {
		return errors.New("static node cluster is required")
	}

	prepareStaticNodeCluster(cluster)
	return store.DeleteStaticNodeCluster(cluster.GetID())
}

func updateStaticNodeClusterStatus(
	store storage.Storage,
	cluster *v1.StaticNodeCluster,
	status v1.StaticNodeClusterStatus,
) error {
	if store == nil {
		return errors.New("storage is required")
	}

	if cluster == nil {
		return errors.New("static node cluster is required")
	}

	prepareStaticNodeCluster(cluster)
	return store.UpdateStaticNodeCluster(cluster.GetID(), &v1.StaticNodeCluster{
		Kind:   staticNodeClusterKind,
		Status: &status,
	})
}

func updateStaticNodeStatus(
	store storage.Storage,
	node *v1.StaticNode,
	status v1.StaticNodeStatus,
) error {
	if store == nil {
		return errors.New("storage is required")
	}

	if node == nil {
		return errors.New("static node is required")
	}

	prepareStaticNode(node)
	return store.UpdateStaticNode(node.GetID(), &v1.StaticNode{
		Kind:   staticNodeKind,
		Status: &status,
	})
}

func findStaticNode(store storage.Storage, workspace, name string) (*v1.StaticNode, bool, error) {
	nodes, err := store.ListStaticNode(storage.ListOption{})
	if err != nil {
		return nil, false, err
	}

	for i := range nodes {
		node := nodes[i]
		if node.Metadata.Workspace == workspace && node.Metadata.Name == name {
			return &node, true, nil
		}
	}

	return nil, false, nil
}

func prepareStaticNodeCluster(cluster *v1.StaticNodeCluster) {
	if cluster.APIVersion == "" {
		cluster.APIVersion = "v1"
	}

	if cluster.Kind == "" {
		cluster.Kind = staticNodeClusterKind
	}
}

func prepareStaticNode(node *v1.StaticNode) {
	if node.APIVersion == "" {
		node.APIVersion = "v1"
	}

	if node.Kind == "" {
		node.Kind = staticNodeKind
	}
}
