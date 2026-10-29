package bench

import (
	"encoding/json"
	"fmt"
	"strings"
)

func modelDiscoveryCommand(baseURL string) string {
	const script = `import sys
from urllib.request import urlopen

url = sys.argv[1].rstrip("/") + "/v1/models"
with urlopen(url, timeout=30) as response:
    sys.stdout.write(response.read().decode("utf-8"))
`

	return "python3 -c " + shellQuote(script) + " " + shellQuote(baseURL)
}

func firstModelName(payload []byte) (string, error) {
	var response struct {
		Data json.RawMessage `json:"data"`
	}

	if err := json.Unmarshal(payload, &response); err != nil {
		return "", fmt.Errorf("decode /v1/models response: %w", err)
	}

	if len(response.Data) == 0 {
		return "", fmt.Errorf("/v1/models response has no data array")
	}

	var models []map[string]json.RawMessage
	if err := json.Unmarshal(response.Data, &models); err != nil {
		return "", fmt.Errorf("decode /v1/models data: %w", err)
	}

	if len(models) == 0 {
		return "", fmt.Errorf("/v1/models response has an empty data array")
	}

	for _, field := range []string{"id", "name", "model"} {
		var value string
		if err := json.Unmarshal(models[0][field], &value); err != nil {
			continue
		}

		if value = strings.TrimSpace(value); value != "" {
			return value, nil
		}
	}

	return "", fmt.Errorf("first /v1/models entry has no id, name, or model")
}
