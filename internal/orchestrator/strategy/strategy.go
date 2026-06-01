// Package strategy validates user-facing endpoint strategy invariants.
package strategy

import (
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// Strategy is the role-topology validator. The public values are standard and
// pd. Future parallelism axes (DP/TP/PP/EP) stay in role variables/options
// until they get a dedicated API.
type Strategy interface {
	Name() string
	Validate(ep *v1.Endpoint) error
}

var (
	standardStrategy = &Standard{}
	pdStrategy       = &PD{}
	registry         = map[string]Strategy{
		"standard": standardStrategy,
		"pd":       pdStrategy,
	}
)

func Register(s Strategy) { registry[s.Name()] = s }

func Get(name string) (Strategy, error) {
	if name == "" {
		name = "standard"
	}

	s, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown strategy %q", name)
	}

	return s, nil
}
