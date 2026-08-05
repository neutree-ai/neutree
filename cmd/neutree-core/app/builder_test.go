package app

import (
	"testing"

	"github.com/gin-gonic/gin"
	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/cmd/neutree-core/app/config"
	"github.com/neutree-ai/neutree/controllers"
	acceleratormocks "github.com/neutree-ai/neutree/internal/accelerator/mocks"
	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
	"github.com/neutree-ai/neutree/internal/cluster/releaseinfo"
)

type testReleaseInfoBuilder struct{}

func (builder *testReleaseInfoBuilder) BuildReleaseInfo(string) (*v1.ReleaseInfo, error) {
	return nil, nil
}

type testCurrentClusterProfileBuilder struct{}

func (builder *testCurrentClusterProfileBuilder) BuildClusterProfile(string) (*v1.ClusterProfile, error) {
	return nil, nil
}

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

type internalTestPlugin struct {
	plugin.AcceleratorPlugin
}

func (internalTestPlugin) Type() string {
	return plugin.InternalPluginType
}

func TestNewBuilderUsesCommunityReleaseInfoBuilder(t *testing.T) {
	builder := NewBuilder()

	if _, ok := builder.releaseInfoBuilder.(*releaseinfo.CommunityReleaseInfoBuilder); !ok {
		t.Fatalf("expected community release info builder, got %T", builder.releaseInfoBuilder)
	}
}

func TestNewBuilderUsesCommunityClusterProfileBuilder(t *testing.T) {
	builder := NewBuilder()

	if _, ok := builder.currentClusterProfileBuilder.(*releaseinfo.CommunityClusterProfileBuilder); !ok {
		t.Fatalf("expected community cluster profile builder, got %T", builder.currentClusterProfileBuilder)
	}
}

func TestNewAppUsesCommunityReleaseInfoBuilder(t *testing.T) {
	application := NewApp(&config.CoreConfig{}, map[string]controllers.Controller{})

	if _, ok := application.releaseInfoBuilder.(*releaseinfo.CommunityReleaseInfoBuilder); !ok {
		t.Fatalf("expected community release info builder, got %T", application.releaseInfoBuilder)
	}
}

func TestNewAppUsesCommunityClusterProfileBuilder(t *testing.T) {
	application := NewApp(&config.CoreConfig{}, map[string]controllers.Controller{})

	if _, ok := application.currentClusterProfileBuilder.(*releaseinfo.CommunityClusterProfileBuilder); !ok {
		t.Fatalf("expected community cluster profile builder, got %T", application.currentClusterProfileBuilder)
	}
}

func TestBuilderBuildPreservesReleaseInfoBuilder(t *testing.T) {
	customBuilder := &testReleaseInfoBuilder{}
	builder := NewBuilder().WithConfig(&config.CoreConfig{GinEngine: gin.New()})
	builder.controllerInits = map[string]ControllerFactory{}
	builder.WithReleaseInfoBuilder(customBuilder)

	application, err := builder.Build()
	if err != nil {
		t.Fatalf("build application: %v", err)
	}

	if application.releaseInfoBuilder != customBuilder {
		t.Fatalf("expected built app to retain custom release info builder, got %T", application.releaseInfoBuilder)
	}
}

func TestBuilderBuildPreservesCurrentClusterProfileBuilder(t *testing.T) {
	customBuilder := &testCurrentClusterProfileBuilder{}
	builder := NewBuilder().WithConfig(&config.CoreConfig{GinEngine: gin.New()})
	builder.controllerInits = map[string]ControllerFactory{}
	builder.WithCurrentClusterProfileBuilder(customBuilder)

	application, err := builder.Build()
	if err != nil {
		t.Fatalf("build application: %v", err)
	}

	if application.currentClusterProfileBuilder != customBuilder {
		t.Fatalf("expected built app to retain custom cluster profile builder, got %T", application.currentClusterProfileBuilder)
	}
}

func TestBuilderWithNilReleaseInfoBuilderUsesCommunityDefault(t *testing.T) {
	builder := NewBuilder().WithReleaseInfoBuilder(nil)

	if _, ok := builder.releaseInfoBuilder.(*releaseinfo.CommunityReleaseInfoBuilder); !ok {
		t.Fatalf("expected community release info builder after nil input, got %T", builder.releaseInfoBuilder)
	}
}

func TestBuilderWithNilCurrentClusterProfileBuilderUsesCommunityDefault(t *testing.T) {
	builder := NewBuilder().WithCurrentClusterProfileBuilder(nil)

	if _, ok := builder.currentClusterProfileBuilder.(*releaseinfo.CommunityClusterProfileBuilder); !ok {
		t.Fatalf("expected community cluster profile builder after nil input, got %T", builder.currentClusterProfileBuilder)
	}
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
