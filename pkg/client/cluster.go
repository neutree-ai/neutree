package client

import (
	"context"
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// GetClusterCredentials reads credentials for one Cluster. Privileged CLI
// workflows should request only the Cluster they are about to operate on.
func (c *Client) GetClusterCredentials(workspace, name string) (*v1.Cluster, error) {
	return c.GetClusterCredentialsContext(context.Background(), workspace, name)
}

// GetClusterCredentialsContext reads credentials with a cancelable request.
func (c *Client) GetClusterCredentialsContext(ctx context.Context, workspace, name string) (*v1.Cluster, error) {
	cluster := &v1.Cluster{}
	credentials := &resourceService{
		client:       c,
		baseUrl:      fmt.Sprintf("%s/api/v1/credentials/clusters", c.baseURL),
		resourceName: "cluster credentials",
	}

	if err := credentials.getContext(ctx, workspace, name, cluster); err != nil {
		return nil, err
	}

	return cluster, nil
}
