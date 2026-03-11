package engine

import v1 "github.com/neutree-ai/neutree/api/v1"

// NewTestRegistry creates a bare Registry without built-in engines or HTTP handlers.
// This is intended for use in tests only.
func NewTestRegistry() Registry {
	return &registry{
		engines: make(map[string]*v1.Engine),
	}
}
