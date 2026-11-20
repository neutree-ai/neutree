package client

import (
	"context"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// GetClusterCredentialsWithCtx reads credentials for one Cluster with a
// cancelable request. Privileged CLI workflows should request only the Cluster
// they are about to operate on.
func (c *Client) GetClusterCredentialsWithCtx(ctx context.Context, workspace, name string) (*v1.Cluster, error) {
	cluster := &v1.Cluster{}

	if err := newCredentialsService(c, "clusters", "cluster credentials").getWithCtx(ctx, workspace, name, cluster); err != nil {
		return nil, err
	}

	return cluster, nil
}
