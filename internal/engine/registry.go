package engine

import (
	"context"
	"sync"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
)

type Registry interface {
	Register(engine *v1.Engine) error

	ListAll(ctx context.Context) ([]*v1.Engine, error)

	Cleanup() error
}

type registry struct {
	mu      sync.RWMutex
	engines map[string]*v1.Engine // key: engine name
}

func NewRegistry() Registry {
	r := &registry{
		engines: make(map[string]*v1.Engine),
	}

	return r
}

func (r *registry) Register(engine *v1.Engine) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if engine.Metadata == nil || engine.Metadata.Name == "" {
		return errors.New("engine name is required")
	}

	if _, existed := r.engines[engine.Metadata.Name]; !existed {
		r.engines[engine.Metadata.Name] = engine
		return nil
	}

	// merge if already exists
	r.engines[engine.Metadata.Name] = util.MergeEngine(r.engines[engine.Metadata.Name], engine)

	return nil
}

func (r *registry) ListAll(ctx context.Context) ([]*v1.Engine, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	engines := make([]*v1.Engine, 0, len(r.engines))
	for _, e := range r.engines {
		engines = append(engines, e)
	}

	return engines, nil
}

func (r *registry) Cleanup() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.engines = make(map[string]*v1.Engine)

	return nil
}
