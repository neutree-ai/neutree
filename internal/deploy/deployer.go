package deploy

import (
	"context"
)

// Deployer is the interface for deploying and managing resources
type Deployer interface {
	// Apply deploys or updates resources
	// Returns the number of changed resources and any error
	Apply(ctx context.Context) (int, error)

	// Delete removes deployed resources
	// Returns true if deletion is complete, false if still in progress
	Delete(ctx context.Context) (bool, error)
}

// DeploymentType represents the type of deployment
type DeploymentType string

const (
	// DeploymentTypeKubernetes represents Kubernetes-based deployment
	DeploymentTypeKubernetes DeploymentType = "kubernetes"
)
