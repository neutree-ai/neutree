package app

import (
	"fmt"

	"k8s.io/klog"

	"github.com/neutree-ai/neutree/cmd/neutree-core/app/config"
	"github.com/neutree-ai/neutree/controllers"
	"github.com/neutree-ai/neutree/internal/accelerator"
	"github.com/neutree-ai/neutree/internal/cluster/releaseinfo"
	publicaccelerator "github.com/neutree-ai/neutree/pkg/accelerator"
)

// Builder is the application builder
type Builder struct {
	controllerInits    map[string]ControllerFactory
	config             *config.CoreConfig
	acceleratorPlugins []publicaccelerator.Plugin

	releaseInfoBuilder           releaseinfo.ReleaseInfoBuilder
	currentClusterProfileBuilder releaseinfo.CurrentClusterProfileBuilder

	beforeHooks       map[string][]controllers.HookFunc
	afterHooks        map[string][]controllers.HookFunc
	globalBeforeHooks []controllers.HookFunc
	globalAfterHooks  []controllers.HookFunc
}

// NewBuilder creates a new CLI builder
func NewBuilder() *Builder {
	b := &Builder{
		controllerInits:              make(map[string]ControllerFactory),
		releaseInfoBuilder:           releaseinfo.NewCommunityReleaseInfoBuilder(),
		currentClusterProfileBuilder: releaseinfo.NewCommunityClusterProfileBuilder(),
		beforeHooks:                  make(map[string][]controllers.HookFunc),
		afterHooks:                   make(map[string][]controllers.HookFunc),
		globalBeforeHooks:            []controllers.HookFunc{},
		globalAfterHooks:             []controllers.HookFunc{},
	}

	defaultControllers := map[string]ControllerFactory{
		"cluster":             NewClusterControllerFactory(),
		"engine":              NewEngineControllerFactory(),
		"endpoint":            NewEndpointControllerFactory(),
		"role":                NewRoleControllerFactory(),
		"role-assignment":     NewRoleAssignmentControllerFactory(),
		"workspace":           NewWorkspaceControllerFactory(),
		"api-key":             NewApiKeyControllerFactory(),
		"image-registry":      NewImageRegistryControllerFactory(),
		"model-catalog":       NewModelCatalogControllerFactory(),
		"model-registry":      NewModelRegistryControllerFactory(),
		"static-node-cluster": NewStaticNodeClusterControllerFactory(),
		"static-node":         NewStaticNodeControllerFactory(),
		"user-profile":        NewUserProfileControllerFactory(),
		"external-endpoint":   NewExternalEndpointControllerFactory(),
	}

	for name, factory := range defaultControllers {
		b.WithController(name, factory)
	}

	return b
}

func (b *Builder) WithConfig(c *config.CoreConfig) *Builder {
	b.config = c
	return b
}

func (b *Builder) WithAcceleratorPlugins(plugins ...publicaccelerator.Plugin) *Builder {
	b.acceleratorPlugins = append(b.acceleratorPlugins, append([]publicaccelerator.Plugin(nil), plugins...)...)
	return b
}

// WithReleaseInfoBuilder configures the builder used for release metadata.
func (b *Builder) WithReleaseInfoBuilder(builder releaseinfo.ReleaseInfoBuilder) *Builder {
	if builder == nil {
		b.releaseInfoBuilder = releaseinfo.NewCommunityReleaseInfoBuilder()
		return b
	}

	b.releaseInfoBuilder = builder
	return b
}

// WithCurrentClusterProfileBuilder configures the builder used for the current cluster profile.
func (b *Builder) WithCurrentClusterProfileBuilder(builder releaseinfo.CurrentClusterProfileBuilder) *Builder {
	if builder == nil {
		b.currentClusterProfileBuilder = releaseinfo.NewCommunityClusterProfileBuilder()
		return b
	}

	b.currentClusterProfileBuilder = builder
	return b
}

// WithController registers a controller factory
func (b *Builder) WithController(name string, factory ControllerFactory) *Builder {
	klog.Info("Registering controller:", name)

	b.controllerInits[name] = factory

	return b
}

func (b *Builder) WithGlobalBeforeReconcileHook(hook controllers.HookFunc) *Builder {
	b.globalBeforeHooks = append(b.globalBeforeHooks, hook)
	return b
}

func (b *Builder) WithGlobalAfterReconcileHook(hook controllers.HookFunc) *Builder {
	b.globalAfterHooks = append(b.globalAfterHooks, hook)
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

	if b.config.GinEngine == nil {
		return nil, fmt.Errorf("gin engine is required")
	}

	if b.config.AcceleratorManager == nil || len(b.acceleratorPlugins) > 0 {
		acceleratorManager, err := accelerator.NewManagerWithPlugins(b.config.GinEngine, b.acceleratorPlugins...)
		if err != nil {
			return nil, fmt.Errorf("create accelerator manager: %w", err)
		}

		b.config.AcceleratorManager = acceleratorManager
	}

	registerControllers := make(map[string]controllers.Controller)
	// Initialize controllers
	for name, factory := range b.controllerInits {
		opts := &ControllerOptions{
			config:      b.config,
			beforeHooks: []controllers.HookFunc{},
			afterHooks:  []controllers.HookFunc{},
			name:        name,
			scheme:      b.config.Scheme,
			storage:     b.config.ObjectStorage,
		}

		opts.afterHooks = append(opts.afterHooks, b.globalAfterHooks...)
		opts.afterHooks = append(opts.afterHooks, b.afterHooks[name]...)
		opts.beforeHooks = append(opts.beforeHooks, b.globalBeforeHooks...)
		opts.beforeHooks = append(opts.beforeHooks, b.beforeHooks[name]...)

		klog.Info("Initializing controller:", name)

		ctrl, err := factory(opts)
		if err != nil {
			return nil, fmt.Errorf("failed to create controller %s: %w", name, err)
		}

		registerControllers[name] = ctrl
	}

	app := NewApp(b.config, registerControllers)
	app.releaseInfoBuilder = b.releaseInfoBuilder
	app.currentClusterProfileBuilder = b.currentClusterProfileBuilder

	return app, nil
}
