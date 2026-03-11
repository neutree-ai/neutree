package engine

import (
	"context"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
	"k8s.io/klog/v2"

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

func NewRegistry(e *gin.Engine) (Registry, error) {
	r := &registry{
		engines: make(map[string]*v1.Engine),
	}

	// register built-in engines
	builtinEngines, err := GetBuiltinEngines()
	if err != nil {
		return nil, errors.Wrap(err, "failed to load built-in engines")
	}

	for _, eng := range builtinEngines {
		if err := r.Register(eng); err != nil {
			return nil, errors.Wrapf(err, "failed to register built-in engine %s", eng.Metadata.Name)
		}

		klog.Infof("Registered built-in engine: %s", eng.Metadata.Name)
	}

	// register external engine registration API
	engineGroup := e.Group("/v1/engine")
	engineGroup.POST("/register", r.registerHandler)

	return r, nil
}

func (r *registry) registerHandler(c *gin.Context) {
	var req v1.RegisterEngineRequest

	if err := c.ShouldBindBodyWithJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	for _, eng := range req.Engines {
		if err := r.Register(eng); err != nil {
			klog.Warningf("failed to register external engine %s: %s", eng.GetName(), err.Error())
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})

			return
		}

		klog.Infof("Registered external engine: %s", eng.GetName())
	}

	c.JSON(http.StatusOK, "ok")
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
