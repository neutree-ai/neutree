package client

import (
	"context"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type StaticNodeStore interface {
	ListStaticNodes(ctx context.Context, workspace, clusterName string) ([]*v1.StaticNode, error)
	UpsertStaticNode(ctx context.Context, node *v1.StaticNode) error
	DeleteStaticNode(ctx context.Context, node *v1.StaticNode) error
	HardDeleteStaticNode(ctx context.Context, node *v1.StaticNode) error
	UpdateStaticNodeStatus(ctx context.Context, node *v1.StaticNode, status v1.StaticNodeStatus) error
}

type StaticNodeClusterStore interface {
	HardDeleteStaticNodeCluster(ctx context.Context, cluster *v1.StaticNodeCluster) error
	UpdateStaticNodeClusterStatus(
		ctx context.Context,
		cluster *v1.StaticNodeCluster,
		status v1.StaticNodeClusterStatus,
	) error
}

type StaticNodeClient struct {
	store StaticNodeStore
}

func NewStaticNodeClient(store StaticNodeStore) *StaticNodeClient {
	return &StaticNodeClient{store: store}
}

func (c *StaticNodeClient) ListByCluster(
	ctx context.Context,
	workspace string,
	clusterName string,
) ([]*v1.StaticNode, error) {
	return c.store.ListStaticNodes(ctx, workspace, clusterName)
}

func (c *StaticNodeClient) Upsert(ctx context.Context, node *v1.StaticNode) error {
	return c.store.UpsertStaticNode(ctx, node)
}

func (c *StaticNodeClient) MarkDeleting(ctx context.Context, node *v1.StaticNode) error {
	return c.store.DeleteStaticNode(ctx, node)
}

func (c *StaticNodeClient) HardDelete(ctx context.Context, node *v1.StaticNode) error {
	return c.store.HardDeleteStaticNode(ctx, node)
}

func (c *StaticNodeClient) UpdateStatus(
	ctx context.Context,
	node *v1.StaticNode,
	status v1.StaticNodeStatus,
) error {
	return c.store.UpdateStaticNodeStatus(ctx, node, status)
}

type StaticNodeClusterClient struct {
	store StaticNodeClusterStore
}

type StaticNodeResourceClient struct {
	storage storage.Storage
}

func NewStaticNodeResourceClient(storage storage.Storage) *StaticNodeResourceClient {
	return &StaticNodeResourceClient{storage: storage}
}

func (c *StaticNodeResourceClient) ListByCluster(
	_ context.Context,
	workspace string,
	clusterName string,
) ([]v1.StaticNode, error) {
	if c == nil || c.storage == nil {
		return nil, errors.New("storage is required")
	}

	filters := []storage.Filter{
		{Column: "spec->>cluster", Operator: "eq", Value: clusterName},
	}
	if workspace != "" {
		filters = append(filters, storage.Filter{
			Column:   "metadata->>workspace",
			Operator: "eq",
			Value:    workspace,
		})
	}

	nodes := []v1.StaticNode{}
	if err := c.storage.GenericQuery(storage.STATIC_NODE_TABLE, "*", filters, &nodes); err != nil {
		return nil, errors.Wrapf(err, "failed to query static nodes for cluster %s/%s resources", workspace, clusterName)
	}

	return nodes, nil
}

func NewStaticNodeClusterClient(store StaticNodeClusterStore) *StaticNodeClusterClient {
	return &StaticNodeClusterClient{store: store}
}

func (c *StaticNodeClusterClient) HardDelete(ctx context.Context, cluster *v1.StaticNodeCluster) error {
	return c.store.HardDeleteStaticNodeCluster(ctx, cluster)
}

func (c *StaticNodeClusterClient) UpdateStatus(
	ctx context.Context,
	cluster *v1.StaticNodeCluster,
	status v1.StaticNodeClusterStatus,
) error {
	return c.store.UpdateStaticNodeClusterStatus(ctx, cluster, status)
}
