package state

import (
	"encoding/json"
	"os"
)

func LoadSeed(path string) (*ConfigEvent, error) {
	b, err := os.ReadFile(path) //nolint:gosec // operator-supplied CLI config path
	if err != nil {
		return nil, err
	}

	var v ConfigEvent
	if err = json.Unmarshal(b, &v); err != nil {
		return nil, err
	}

	return &v, nil
}
