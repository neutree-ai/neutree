package app

import (
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/neutree-ai/neutree/cmd/neutree-core/app/config"
	acceleratormocks "github.com/neutree-ai/neutree/internal/accelerator/mocks"
	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
)

func TestNewBuilder(t *testing.T) {
	builder := NewBuilder()
	if builder == nil {
		t.Fatal("Expected NewBuilder to return a non-nil Builder")
	}

	if len(builder.controllerInits) == 0 {
		t.Error("Expected NewBuilder to register default controllerInits")
	}

	for _, name := range []string{"static-node-cluster", "static-node"} {
		if _, exists := builder.controllerInits[name]; !exists {
			t.Errorf("Expected NewBuilder to register %q controller", name)
		}
	}
}

func TestBuilderWithConfig(t *testing.T) {
	builder := NewBuilder()
	config := &config.CoreConfig{}
	builder.WithConfig(config)

	if builder.config != config {
		t.Errorf("Expected config to be set in builder, got %v", builder.config)
	}
}

func TestBuilderBuildInjectsAcceleratorPlugins(t *testing.T) {
	config := &config.CoreConfig{GinEngine: gin.New()}
	injected := internalTestPlugin{AcceleratorPlugin: plugin.NewAcceleratorRestPlugin("injected-test", "http://plugin.example")}
	builder := NewBuilder().WithConfig(config).WithAcceleratorPlugins(injected)
	builder.controllerInits = map[string]ControllerFactory{}

	_, err := builder.Build()

	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if config.AcceleratorManager == nil {
		t.Fatal("Build() did not initialize AcceleratorManager")
	}
	if _, ok := config.AcceleratorManager.GetPlugin("injected-test"); !ok {
		t.Fatal("Build() did not register the injected accelerator plugin")
	}
}

func TestBuilderBuildRequiresGinEngine(t *testing.T) {
	builder := NewBuilder().WithConfig(&config.CoreConfig{})
	builder.controllerInits = map[string]ControllerFactory{}

	_, err := builder.Build()

	if err == nil {
		t.Fatal("Build() error = nil, want GinEngine validation error")
	}
}

func TestBuilderBuildPreservesConfiguredAcceleratorManagerWithoutInjectedPlugins(t *testing.T) {
	manager := &acceleratormocks.MockManager{}
	config := &config.CoreConfig{AcceleratorManager: manager}
	builder := NewBuilder().WithConfig(config)
	builder.controllerInits = map[string]ControllerFactory{}

	_, err := builder.Build()

	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if config.AcceleratorManager != manager {
		t.Fatal("Build() replaced the configured AcceleratorManager without injected plugins")
	}
}

type internalTestPlugin struct {
	plugin.AcceleratorPlugin
}

func (internalTestPlugin) Type() string {
	return plugin.InternalPluginType
}

func TestBuilderWithController(t *testing.T) {
	builder := NewBuilder()
	controllerFactory := NewClusterControllerFactory()
	builder.WithController("test-controller", controllerFactory)

	if _, exists := builder.controllerInits["test-controller"]; !exists {
		t.Error("Expected controller 'test-controller' to be registered in builder")
	}
}

func TestBuilderWithGlobalAfterReconcileHook(t *testing.T) {
	builder := NewBuilder()
	hook := func(obj interface{}) error {
		return nil
	}

	builder.WithGlobalAfterReconcileHook(hook)

	for name, hooks := range builder.afterHooks {
		if len(hooks) == 0 {
			t.Errorf("Expected after hooks for controller '%s' to be registered", name)
		}
	}
}

func TestBuilderWithBeforeReconcileHook(t *testing.T) {
	builder := NewBuilder()
	hook := func(obj interface{}) error {
		return nil
	}

	builder.WithBeforeReconcileHook("test-controller", hook)

	if hooks, exists := builder.beforeHooks["test-controller"]; !exists || len(hooks) == 0 {
		t.Error("Expected before hooks for 'test-controller' to be registered in builder")
	}
}

func TestBuilderWithAfterReconcileHook(t *testing.T) {
	builder := NewBuilder()
	hook := func(obj interface{}) error {
		return nil
	}

	builder.WithAfterReconcileHook("test-controller", hook)

	if hooks, exists := builder.afterHooks["test-controller"]; !exists || len(hooks) == 0 {
		t.Error("Expected after hooks for 'test-controller' to be registered in builder")
	}
}
