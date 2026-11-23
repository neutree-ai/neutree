package utils

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const maxModelListResponseBytes = 1 << 20

// FetchModelIDs reads an OpenAI-compatible model list using the caller's request and HTTP policy.
// IDs retain their exact values and order; callers decide how to handle empty or duplicate IDs.
func FetchModelIDs(client *http.Client, req *http.Request) ([]string, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("connection failed: %w", err)
	}

	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxModelListResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read model list: %w", err)
	}

	if len(body) > maxModelListResponseBytes {
		return nil, fmt.Errorf("model list response exceeds size limit")
	}

	if resp.StatusCode != http.StatusOK {
		message := fmt.Sprintf("upstream returned HTTP %d", resp.StatusCode)
		if len(body) > 0 {
			message += ": " + string(body)
		}

		return nil, fmt.Errorf("%s", message)
	}

	models, err := parseModelIDs(body)
	if err != nil {
		return nil, fmt.Errorf("upstream response is not a valid OpenAI-compatible model list: %w", err)
	}

	return models, nil
}

func parseModelIDs(body []byte) ([]string, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	dataRaw, ok := raw["data"]
	if !ok {
		return nil, fmt.Errorf("missing \"data\" field")
	}

	var data []struct {
		ID string `json:"id"`
	}

	if err := json.Unmarshal(dataRaw, &data); err != nil {
		return nil, fmt.Errorf("\"data\" is not a valid array: %w", err)
	}

	if data == nil {
		return nil, fmt.Errorf("\"data\" is not a valid array")
	}

	ids := make([]string, 0, len(data))

	for _, model := range data {
		ids = append(ids, model.ID)
	}

	return ids, nil
}
