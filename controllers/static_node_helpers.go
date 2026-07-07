package controllers

import (
	"reflect"
	"time"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

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

	return store.UpdateStaticNode(existing.GetID(), staticNodeDesiredUpdate(node))
}

func validateStaticNodeOwner(existing *v1.StaticNode, desired *v1.StaticNode) error {
	if existing == nil || desired == nil || existing.Spec == nil || desired.Spec == nil {
		return nil
	}

	if existing.Spec.Cluster == "" || desired.Spec.Cluster == "" || existing.Spec.Cluster == desired.Spec.Cluster {
		return nil
	}

	return errors.Errorf("static node %s is already owned by static node cluster %s",
		desired.Metadata.Name, existing.Spec.Cluster)
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

	return store.UpdateStaticNode(node.GetID(), staticNodeMetadataUpdate(node))
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

	if node.Status != nil && reflect.DeepEqual(*node.Status, status) {
		return nil
	}

	return store.UpdateStaticNode(node.GetID(), &v1.StaticNode{
		Status: &status,
	})
}

func staticNodeDesiredUpdate(node *v1.StaticNode) *v1.StaticNode {
	if node == nil {
		return nil
	}

	return &v1.StaticNode{
		ID:         node.ID,
		APIVersion: node.APIVersion,
		Kind:       node.Kind,
		Metadata:   node.Metadata,
		Spec:       node.Spec,
	}
}

func staticNodeMetadataUpdate(node *v1.StaticNode) *v1.StaticNode {
	if node == nil {
		return nil
	}

	return &v1.StaticNode{
		ID:         node.ID,
		APIVersion: node.APIVersion,
		Kind:       node.Kind,
		Metadata:   node.Metadata,
	}
}

func findStaticNode(store storage.Storage, workspace, name string) (*v1.StaticNode, bool, error) {
	nodes, err := store.ListStaticNode(storage.ListOption{
		Filters: []storage.Filter{
			{Column: "metadata->>workspace", Operator: "eq", Value: workspace},
			{Column: "metadata->>name", Operator: "eq", Value: name},
		},
	})
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
		cluster.Kind = v1.StaticNodeClusterKind
	}
}

func prepareStaticNode(node *v1.StaticNode) {
	if node.APIVersion == "" {
		node.APIVersion = "v1"
	}

	if node.Kind == "" {
		node.Kind = v1.StaticNodeKind
	}
}
