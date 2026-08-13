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
		if eng == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "engine entry must not be nil"})
			return
		}

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

	if engine == nil {
		return errors.New("engine must not be nil")
	}

	if engine.Metadata == nil || engine.Metadata.Name == "" {
		return errors.New("engine name is required")
	}

	if engine.Spec == nil {
		return errors.New("engine spec is required")
	}

	// Reject unusable declarations here rather than letting them reach the
	// consumers, which would silently ignore them.
	for i, version := range engine.Spec.Versions {
		// A nil entry cannot merely be skipped: it would be stored, and the
		// next registration of the same engine dereferences every version in
		// util.MergeEngine, panicking the /v1/engine/register handler.
		if version == nil {
			return errors.Errorf("engine %s version entry %d must not be nil", engine.Metadata.Name, i)
		}

		if err := version.Capabilities.Validate(); err != nil {
			return errors.Wrapf(err, "engine %s version %s declares invalid capabilities",
				engine.Metadata.Name, version.Version)
		}
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
