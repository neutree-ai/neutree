package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/cmd/neutree-core/app/config"
	"github.com/neutree-ai/neutree/internal/accelerator"
	acceleratormocks "github.com/neutree-ai/neutree/internal/accelerator/mocks"
	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
	"github.com/neutree-ai/neutree/internal/accelerator/resourceparser"
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
	injected := internalTestPlugin{}
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

func TestBuilderBuildDoesNotRegisterExternalPluginEndpoint(t *testing.T) {
	engine := gin.New()
	config := &config.CoreConfig{GinEngine: engine}
	builder := NewBuilder().WithConfig(config)
	builder.controllerInits = map[string]ControllerFactory{}

	_, err := builder.Build()

	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	// The external REST accelerator plugin registration endpoint was removed:
	// POST /v1/plugin/register must no longer be served (stable 404, no panic).
	req := httptest.NewRequest(http.MethodPost, "/v1/plugin/register", strings.NewReader(`{"resource_name":"external_gpu","endpoint":"http://plugin.example"}`))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("POST /v1/plugin/register status = %d, want 404", recorder.Code)
	}
}

func TestBuilderBuildRegistersInjectedPluginsOnExistingManager(t *testing.T) {
	existingManager := accelerator.NewManager()
	config := &config.CoreConfig{GinEngine: gin.New(), AcceleratorManager: existingManager}
	injected := internalTestPlugin{}
	builder := NewBuilder().WithConfig(config).WithAcceleratorPlugins(injected)
	builder.controllerInits = map[string]ControllerFactory{}

	if _, err := builder.Build(); err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if config.AcceleratorManager != existingManager {
		t.Fatal("Build() replaced the configured AcceleratorManager with injected plugins")
	}
	if _, ok := config.AcceleratorManager.GetPlugin("injected-test"); !ok {
		t.Fatal("Build() did not register the injected accelerator plugin into the existing manager")
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
	config := &config.CoreConfig{GinEngine: gin.New(), AcceleratorManager: manager}
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

func TestBuilderBuildRequiresGinEngineWithConfiguredAcceleratorManager(t *testing.T) {
	builder := NewBuilder().WithConfig(&config.CoreConfig{AcceleratorManager: &acceleratormocks.MockManager{}})
	builder.controllerInits = map[string]ControllerFactory{}

	_, err := builder.Build()

	if err == nil {
		t.Fatal("Build() error = nil, want GinEngine validation error")
	}
}

type internalTestPlugin struct{}

func (internalTestPlugin) Resource() string {
	return "injected-test"
}

func (internalTestPlugin) Type() string {
	return plugin.InternalPluginType
}

func (internalTestPlugin) Handle() plugin.AcceleratorPluginHandle {
	return internalTestPlugin{}
}

func (internalTestPlugin) GetNodeAccelerator(context.Context, *v1.GetNodeAcceleratorRequest) (*v1.GetNodeAcceleratorResponse, error) {
	return nil, nil
}

func (internalTestPlugin) GetNodeRuntimeConfig(context.Context, *v1.GetNodeRuntimeConfigRequest) (*v1.GetNodeRuntimeConfigResponse, error) {
	return nil, nil
}

func (internalTestPlugin) DetectStaticNodeAccelerator(context.Context, *v1.DetectStaticNodeAcceleratorRequest) (*v1.DetectStaticNodeAcceleratorResponse, error) {
	return nil, nil
}

func (internalTestPlugin) GetContainerRuntimeConfig() (v1.RuntimeConfig, error) {
	return v1.RuntimeConfig{}, nil
}

func (internalTestPlugin) GetAcceleratorProfile(context.Context) (*v1.AcceleratorProfile, error) {
	return nil, nil
}

func (internalTestPlugin) GetResourceConverter() plugin.ResourceConverter {
	return nil
}

func (internalTestPlugin) GetResourceParser() resourceparser.ResourceParser {
	return nil
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
