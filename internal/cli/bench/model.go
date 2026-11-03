package bench

import (
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// firstServedModel returns the first serving name the endpoint controller
// recorded from the inference instance's /v1/models response. The benchmark
// sends that name in each request, so the route resolves it exactly as a
// client would.
func firstServedModel(endpoint *v1.Endpoint) (string, error) {
	if len(endpoint.Status.ServedModels) == 0 {
		return "", fmt.Errorf("Endpoint %q reports no served models", endpoint.Metadata.Name)
	}

	return endpoint.Status.ServedModels[0], nil
}
