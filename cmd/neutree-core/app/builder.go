package app

import (
	"fmt"

	"github.com/neutree-ai/neutree/cmd/neutree-core/app/config"
	"github.com/neutree-ai/neutree/controllers"
)

// Builder is the application builder
type Builder struct {
	controllerInits map[string]ControllerFactory
	config          *config.CoreConfig

	beforeHooks map[string][]controllers.HookFunc
	afterHooks  map[string][]controllers.HookFunc
}

// NewBuilder creates a new CLI builder
func NewBuilder() *Builder {
	b := &Builder{
		controllerInits: make(map[string]ControllerFactory),
		beforeHooks:     make(map[string][]controllers.HookFunc),
		afterHooks:      make(map[string][]controllers.HookFunc),
	}

	defaultControllers := map[string]ControllerFactory{
		"cluster":         NewClusterControllerFactory(),
		"engine":          NewEngineControllerFactory(),
		"endpoint":        NewEndpointControllerFactory(),
		"role":            NewRoleControllerFactory(),
		"role-assignment": NewRoleAssignmentControllerFactory(),
		"workspace":       NewWorkspaceControllerFactory(),
		"api-key":         NewApiKeyControllerFactory(),
		"image-registry":  NewImageRegistryControllerFactory(),
		"model-catalog":   NewModelCatalogControllerFactory(),
		"model-registry":  NewModelRegistryControllerFactory(),
	}

	for name, factory := range defaultControllers {
		b.controllerInits[name] = factory
	}

	return b
}

func (b *Builder) WithConfig(c *config.CoreConfig) *Builder {
	b.config = c
	return b
}

// WithController registers a controller factory
func (b *Builder) WithController(name string, factory ControllerFactory) *Builder {
	b.controllerInits[name] = factory
	return b
}

func (b *Builder) WithGlobalBeforeReconcileHook(hook controllers.HookFunc) *Builder {
	for name := range b.controllerInits {
		if _, exists := b.beforeHooks[name]; !exists {
			b.beforeHooks[name] = []controllers.HookFunc{}
		}

		b.beforeHooks[name] = append(b.beforeHooks[name], hook)
	}

	return b
}

func (b *Builder) WithGlobalAfterReconcileHook(hook controllers.HookFunc) *Builder {
	for name := range b.controllerInits {
		if _, exists := b.afterHooks[name]; !exists {
			b.afterHooks[name] = []controllers.HookFunc{}
		}

		b.afterHooks[name] = append(b.afterHooks[name], hook)
	}

	return b
}

// WithBeforeReconcileHook registers a before reconcile hook for a controller
func (b *Builder) WithBeforeReconcileHook(controllerName string, hook controllers.HookFunc) *Builder {
	if _, exists := b.beforeHooks[controllerName]; !exists {
		b.beforeHooks[controllerName] = []controllers.HookFunc{}
	}

	b.beforeHooks[controllerName] = append(b.beforeHooks[controllerName], hook)

	return b
}

// WithAfterReconcileHook registers an after reconcile hook for a controller
func (b *Builder) WithAfterReconcileHook(controllerName string, hook controllers.HookFunc) *Builder {
	if _, exists := b.afterHooks[controllerName]; !exists {
		b.afterHooks[controllerName] = []controllers.HookFunc{}
	}

	b.afterHooks[controllerName] = append(b.afterHooks[controllerName], hook)

	return b
}

// Build creates and initializes all components
func (b *Builder) Build() (*App, error) {
	if b.config == nil {
		return nil, fmt.Errorf("configuration is required to build the application")
	}

	controllers := make(map[string]controllers.Controller)
	// Initialize controllers
	for name, factory := range b.controllerInits {
		opts := &ControllerOptions{
			config:      b.config,
			beforeHooks: b.beforeHooks[name],
			afterHooks:  b.afterHooks[name],
			name:        name,
		}

		ctrl, err := factory(opts)
		if err != nil {
			return nil, fmt.Errorf("failed to create controller %s: %w", name, err)
		}

		controllers[name] = ctrl
	}

	return NewApp(b.config, controllers), nil
}
