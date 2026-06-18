package util

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func HashString(s string) string {
	hash := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", hash)[:10]
}

func ComputeEndpointModelHash(endpoint *v1.Endpoint) (string, error) {
	if endpoint == nil || endpoint.Spec == nil || endpoint.Spec.Model == nil {
		return "", nil
	}

	modelJSON, err := json.Marshal(endpoint.Spec.Model)
	if err != nil {
		return "", err
	}

	hash := sha256.Sum256(modelJSON)

	return fmt.Sprintf("%x", hash), nil
}
