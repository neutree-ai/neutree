package framework

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2" //nolint:revive,stylecheck
	v1 "github.com/neutree-ai/neutree/api/v1"
)

// WaitOptions configures the wait behavior.
type WaitOptions struct {
	Timeout  time.Duration
	Interval time.Duration
}

// DefaultWaitOptions provides sensible defaults for waiting.
var DefaultWaitOptions = WaitOptions{
	Timeout:  5 * time.Minute,
	Interval: 5 * time.Second,
}

func applyDefaults(opts *WaitOptions) {
	if opts.Timeout == 0 {
		opts.Timeout = DefaultWaitOptions.Timeout
	}
	if opts.Interval == 0 {
		opts.Interval = DefaultWaitOptions.Interval
	}
}

// WaitForImageRegistry waits for an image registry to reach the expected phase.
func (c *Client) WaitForImageRegistry(workspace, name string, expectedPhase v1.ImageRegistryPhase, opts WaitOptions) error {
	applyDefaults(&opts)
	deadline := time.Now().Add(opts.Timeout)
	var lastErr error

	for time.Now().Before(deadline) {
		ir, err := c.GetImageRegistry(workspace, name)
		if err != nil {
			lastErr = err
			GinkgoWriter.Printf("Warning: transient error getting image registry %s/%s: %v\n", workspace, name, err)
			time.Sleep(opts.Interval)
			continue
		}

		if ir.Status != nil {
			if ir.Status.Phase == expectedPhase {
				return nil
			}
			if ir.Status.Phase == v1.ImageRegistryPhaseFAILED {
				return fmt.Errorf("image registry failed: %s", ir.Status.ErrorMessage)
			}
		}

		time.Sleep(opts.Interval)
	}

	if lastErr != nil {
		return fmt.Errorf("timeout waiting for image registry %s/%s to reach phase %s (last error: %w)", workspace, name, expectedPhase, lastErr)
	}
	return fmt.Errorf("timeout waiting for image registry %s/%s to reach phase %s", workspace, name, expectedPhase)
}

// WaitForCluster waits for a cluster to reach the expected phase and initialization state.
func (c *Client) WaitForCluster(workspace, name string, expectedPhase v1.ClusterPhase, initialized bool, opts WaitOptions) error {
	applyDefaults(&opts)
	deadline := time.Now().Add(opts.Timeout)
	var lastErr error

	for time.Now().Before(deadline) {
		cluster, err := c.GetCluster(workspace, name)
		if err != nil {
			lastErr = err
			GinkgoWriter.Printf("Warning: transient error getting cluster %s/%s: %v\n", workspace, name, err)
			time.Sleep(opts.Interval)
			continue
		}

		if cluster.Status != nil {
			if cluster.Status.Phase == expectedPhase {
				if !initialized || cluster.Status.Initialized {
					return nil
				}
			}
			if cluster.Status.Phase == v1.ClusterPhaseFailed {
				return fmt.Errorf("cluster failed: %s", cluster.Status.ErrorMessage)
			}
		}

		time.Sleep(opts.Interval)
	}

	if lastErr != nil {
		return fmt.Errorf("timeout waiting for cluster %s/%s to reach phase %s (initialized=%v) (last error: %w)",
			workspace, name, expectedPhase, initialized, lastErr)
	}
	return fmt.Errorf("timeout waiting for cluster %s/%s to reach phase %s (initialized=%v)", workspace, name, expectedPhase, initialized)
}

// WaitForModelRegistry waits for a model registry to reach the expected phase.
func (c *Client) WaitForModelRegistry(workspace, name string, expectedPhase v1.ModelRegistryPhase, opts WaitOptions) error {
	applyDefaults(&opts)
	deadline := time.Now().Add(opts.Timeout)
	var lastErr error

	for time.Now().Before(deadline) {
		mr, err := c.GetModelRegistry(workspace, name)
		if err != nil {
			lastErr = err
			GinkgoWriter.Printf("Warning: transient error getting model registry %s/%s: %v\n", workspace, name, err)
			time.Sleep(opts.Interval)
			continue
		}

		if mr.Status != nil {
			if mr.Status.Phase == expectedPhase {
				return nil
			}
			if mr.Status.Phase == v1.ModelRegistryPhaseFAILED {
				return fmt.Errorf("model registry failed: %s", mr.Status.ErrorMessage)
			}
		}

		time.Sleep(opts.Interval)
	}

	if lastErr != nil {
		return fmt.Errorf("timeout waiting for model registry %s/%s to reach phase %s (last error: %w)", workspace, name, expectedPhase, lastErr)
	}
	return fmt.Errorf("timeout waiting for model registry %s/%s to reach phase %s", workspace, name, expectedPhase)
}

// WaitForEndpoint waits for an endpoint to reach the expected phase and returns the endpoint.
func (c *Client) WaitForEndpoint(workspace, name string, expectedPhase v1.EndpointPhase, opts WaitOptions) (*v1.Endpoint, error) {
	applyDefaults(&opts)
	deadline := time.Now().Add(opts.Timeout)
	var lastErr error

	for time.Now().Before(deadline) {
		ep, err := c.GetEndpoint(workspace, name)
		if err != nil {
			lastErr = err
			GinkgoWriter.Printf("Warning: transient error getting endpoint %s/%s: %v\n", workspace, name, err)
			time.Sleep(opts.Interval)
			continue
		}

		if ep.Status != nil {
			if ep.Status.Phase == expectedPhase {
				return ep, nil
			}
			if ep.Status.Phase == v1.EndpointPhaseFAILED {
				return nil, fmt.Errorf("endpoint failed: %s", ep.Status.ErrorMessage)
			}
		}

		time.Sleep(opts.Interval)
	}

	if lastErr != nil {
		return nil, fmt.Errorf("timeout waiting for endpoint %s/%s to reach phase %s (last error: %w)", workspace, name, expectedPhase, lastErr)
	}
	return nil, fmt.Errorf("timeout waiting for endpoint %s/%s to reach phase %s", workspace, name, expectedPhase)
}

// WaitForEndpointDeleted waits for an endpoint to be fully deleted.
func (c *Client) WaitForEndpointDeleted(workspace, name string, opts WaitOptions) error {
	applyDefaults(&opts)
	deadline := time.Now().Add(opts.Timeout)

	for time.Now().Before(deadline) {
		ep, err := c.GetEndpoint(workspace, name)
		if err != nil {
			// Resource not found is expected when deleted
			return nil
		}

		if ep.Status != nil && ep.Status.Phase == v1.EndpointPhaseDELETED {
			return nil
		}

		time.Sleep(opts.Interval)
	}

	return fmt.Errorf("timeout waiting for endpoint %s/%s to be deleted", workspace, name)
}

// WaitForClusterDeleted waits for a cluster to be fully deleted.
func (c *Client) WaitForClusterDeleted(workspace, name string, opts WaitOptions) error {
	applyDefaults(&opts)
	deadline := time.Now().Add(opts.Timeout)

	for time.Now().Before(deadline) {
		cluster, err := c.GetCluster(workspace, name)
		if err != nil {
			// Resource not found is expected when deleted
			return nil
		}

		if cluster.Status != nil && cluster.Status.Phase == v1.ClusterPhaseDeleted {
			return nil
		}

		time.Sleep(opts.Interval)
	}

	return fmt.Errorf("timeout waiting for cluster %s/%s to be deleted", workspace, name)
}
