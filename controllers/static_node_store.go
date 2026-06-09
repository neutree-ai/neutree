package controllers

import (
	"context"
	"strings"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	clusterreconcile "github.com/neutree-ai/neutree/internal/cluster"
	"github.com/neutree-ai/neutree/pkg/storage"
)

const (
	staticNodeClusterKind = "StaticNodeCluster"
	staticNodeKind        = "StaticNode"
	staticNodeListKind    = "StaticNodeList"
)

var (
	_ clusterreconcile.StaticNodeClusterStore = (*StaticNodeObjectStore)(nil)
	_ clusterreconcile.StaticNodeStore        = (*StaticNodeObjectStore)(nil)
)

type StaticNodeObjectStore struct {
	Storage storage.ObjectStorage
}

func NewStaticNodeObjectStore(objectStorage storage.ObjectStorage) *StaticNodeObjectStore {
	return &StaticNodeObjectStore{Storage: objectStorage}
}

func (s *StaticNodeObjectStore) ListStaticNodes(
	_ context.Context,
	workspace string,
	clusterName string,
) ([]*v1.StaticNode, error) {
	if s == nil || s.Storage == nil {
		return nil, errors.New("object storage is required")
	}

	list := &v1.StaticNodeList{Kind: staticNodeListKind}
	if err := s.Storage.List(list, storage.ListOption{}); err != nil {
		return nil, err
	}

	nodes := make([]*v1.StaticNode, 0, len(list.Items))
	for i := range list.Items {
		node := list.Items[i]
		if node.Metadata == nil || node.Spec == nil {
			continue
		}

		if node.Metadata.Workspace != workspace || node.Spec.Cluster != clusterName {
			continue
		}

		nodes = append(nodes, &node)
	}

	return nodes, nil
}

func (s *StaticNodeObjectStore) UpsertStaticNode(_ context.Context, node *v1.StaticNode) error {
	if s == nil || s.Storage == nil {
		return errors.New("object storage is required")
	}

	if node == nil || node.Metadata == nil {
		return errors.New("static node metadata is required")
	}

	prepareStaticNode(node)

	existing, found, err := s.findStaticNode(node.Metadata.Workspace, node.Metadata.Name)
	if err != nil {
		return err
	}

	if !found {
		return s.Storage.Create(node)
	}

	node.ID = existing.ID
	id := existing.GetID()
	if emptyObjectID(id) {
		return errors.New("existing static node id is required")
	}

	if err := s.Storage.UpdateMetadata(id, node); err != nil {
		return err
	}

	return s.Storage.UpdateSpec(id, node)
}

func (s *StaticNodeObjectStore) DeleteStaticNode(_ context.Context, node *v1.StaticNode) error {
	if s == nil || s.Storage == nil {
		return errors.New("object storage is required")
	}

	if node == nil {
		return errors.New("static node is required")
	}

	prepareStaticNode(node)

	id := node.GetID()
	if emptyObjectID(id) {
		if node.Metadata == nil {
			return errors.New("static node metadata is required")
		}

		existing, found, err := s.findStaticNode(node.Metadata.Workspace, node.Metadata.Name)
		if err != nil {
			return err
		}

		if !found {
			return nil
		}

		id = existing.GetID()
	}
	if emptyObjectID(id) {
		return errors.New("static node id is required")
	}

	return s.Storage.Delete(id, &v1.StaticNode{Kind: staticNodeKind})
}

func (s *StaticNodeObjectStore) UpdateStaticNodeClusterStatus(
	_ context.Context,
	cluster *v1.StaticNodeCluster,
	status v1.StaticNodeClusterStatus,
) error {
	if s == nil || s.Storage == nil {
		return errors.New("object storage is required")
	}

	if cluster == nil {
		return errors.New("static node cluster is required")
	}

	prepareStaticNodeCluster(cluster)
	id := cluster.GetID()
	if emptyObjectID(id) {
		return errors.New("static node cluster id is required")
	}

	return s.Storage.UpdateStatus(id, &v1.StaticNodeCluster{
		Kind:   staticNodeClusterKind,
		Status: &status,
	})
}

func (s *StaticNodeObjectStore) UpdateStaticNodeStatus(
	_ context.Context,
	node *v1.StaticNode,
	status v1.StaticNodeStatus,
) error {
	if s == nil || s.Storage == nil {
		return errors.New("object storage is required")
	}

	if node == nil {
		return errors.New("static node is required")
	}

	prepareStaticNode(node)
	id := node.GetID()
	if emptyObjectID(id) {
		return errors.New("static node id is required")
	}

	return s.Storage.UpdateStatus(id, &v1.StaticNode{
		Kind:   staticNodeKind,
		Status: &status,
	})
}

func (s *StaticNodeObjectStore) findStaticNode(workspace, name string) (*v1.StaticNode, bool, error) {
	list := &v1.StaticNodeList{Kind: staticNodeListKind}
	if err := s.Storage.List(list, storage.ListOption{}); err != nil {
		return nil, false, err
	}

	for i := range list.Items {
		node := list.Items[i]
		if node.Metadata == nil {
			continue
		}

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

func emptyObjectID(id string) bool {
	id = strings.TrimSpace(id)

	return id == "" || id == "0"
}
