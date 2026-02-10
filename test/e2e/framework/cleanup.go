package framework

import (
	"fmt"
	"time"
)

// CleanupList specifies resources to clean up.
type CleanupList struct {
	Endpoints       []string
	Clusters        []string
	ModelRegistries []string
	ImageRegistries []string
}

// CleanupResources deletes resources in reverse dependency order.
// It soft-deletes each resource and waits for deletion to complete.
func (c *Client) CleanupResources(workspace string, resources CleanupList) error {
	var errs []error

	waitOpts := WaitOptions{
		Timeout:  5 * time.Minute,
		Interval: 5 * time.Second,
	}

	// Delete endpoints first (depend on clusters)
	for _, name := range resources.Endpoints {
		if err := c.DeleteEndpoint(workspace, name); err != nil {
			errs = append(errs, fmt.Errorf("failed to delete endpoint %s: %w", name, err))
			continue
		}
		if err := c.WaitForEndpointDeleted(workspace, name, waitOpts); err != nil {
			errs = append(errs, fmt.Errorf("failed waiting for endpoint %s deletion: %w", name, err))
		}
	}

	// Delete clusters (depend on image registries)
	for _, name := range resources.Clusters {
		if err := c.DeleteCluster(workspace, name); err != nil {
			errs = append(errs, fmt.Errorf("failed to delete cluster %s: %w", name, err))
			continue
		}
		if err := c.WaitForClusterDeleted(workspace, name, waitOpts); err != nil {
			errs = append(errs, fmt.Errorf("failed waiting for cluster %s deletion: %w", name, err))
		}
	}

	// Delete model registries
	for _, name := range resources.ModelRegistries {
		if err := c.DeleteModelRegistry(workspace, name); err != nil {
			errs = append(errs, fmt.Errorf("failed to delete model registry %s: %w", name, err))
		}
	}

	// Delete image registries last
	for _, name := range resources.ImageRegistries {
		if err := c.DeleteImageRegistry(workspace, name); err != nil {
			errs = append(errs, fmt.Errorf("failed to delete image registry %s: %w", name, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("cleanup errors: %v", errs)
	}

	return nil
}

// CleanupResourcesIgnoreErrors deletes resources but ignores errors.
// Useful for AfterEach/AfterAll cleanup where we want to clean up as much as possible.
func (c *Client) CleanupResourcesIgnoreErrors(workspace string, resources CleanupList) {
	waitOpts := WaitOptions{
		Timeout:  5 * time.Minute,
		Interval: 5 * time.Second,
	}

	// Delete endpoints first (depend on clusters)
	for _, name := range resources.Endpoints {
		if err := c.DeleteEndpoint(workspace, name); err != nil {
			continue
		}
		_ = c.WaitForEndpointDeleted(workspace, name, waitOpts)
	}

	// Delete clusters (depend on image registries)
	for _, name := range resources.Clusters {
		if err := c.DeleteCluster(workspace, name); err != nil {
			continue
		}
		_ = c.WaitForClusterDeleted(workspace, name, waitOpts)
	}

	// Delete model registries
	for _, name := range resources.ModelRegistries {
		_ = c.DeleteModelRegistry(workspace, name)
	}

	// Delete image registries last
	for _, name := range resources.ImageRegistries {
		_ = c.DeleteImageRegistry(workspace, name)
	}
}
