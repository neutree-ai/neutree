package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/cmd/neutree-core/app/config"
	"github.com/neutree-ai/neutree/controllers"
	"github.com/neutree-ai/neutree/internal/accelerator"
	acceleratormocks "github.com/neutree-ai/neutree/internal/accelerator/mocks"
	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
	"github.com/neutree-ai/neutree/internal/accelerator/resourceparser"
	"github.com/neutree-ai/neutree/pkg/releaseprofile"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

type testReleaseInfoBuilder struct{}

func (builder *testReleaseInfoBuilder) BuildReleaseInfo(string) (*v1.ReleaseInfo, error) {
	return nil, nil
}

func (builder *testReleaseInfoBuilder) CurrentReleaseInfoBaseline() string {
	return "v1.2.0"
}

func (builder *testReleaseInfoBuilder) BuildClusterProfiles(string) ([]*v1.ClusterProfile, error) {
	return nil, nil
}

func (builder *testReleaseInfoBuilder) BuildPackageImages(string, string, string) ([]v1.ImageRef, error) {
	return nil, nil
}

func (builder *testReleaseInfoBuilder) PackageAccelerators(string) []string {
	return nil
}

type testReleaseInfoBuilderWithBaseline struct {
	baseline string
}

func (builder *testReleaseInfoBuilderWithBaseline) BuildReleaseInfo(string) (*v1.ReleaseInfo, error) {
	return nil, nil
}

func (builder *testReleaseInfoBuilderWithBaseline) CurrentReleaseInfoBaseline() string {
	return builder.baseline
}

func (builder *testReleaseInfoBuilderWithBaseline) BuildClusterProfiles(string) ([]*v1.ClusterProfile, error) {
	return nil, nil
}

func (builder *testReleaseInfoBuilderWithBaseline) BuildPackageImages(string, string, string) ([]v1.ImageRef, error) {
	return nil, nil
}

func (builder *testReleaseInfoBuilderWithBaseline) PackageAccelerators(string) []string {
	return nil
}

type testCurrentClusterProfileBuilder struct{}

func (builder *testCurrentClusterProfileBuilder) BuildClusterProfiles(string) ([]*v1.ClusterProfile, error) {
	return nil, nil
}

func (builder *testCurrentClusterProfileBuilder) CurrentReleaseInfoBaseline() string {
	return "v1.2.0"
}

func (builder *testCurrentClusterProfileBuilder) BuildReleaseInfo(string) (*v1.ReleaseInfo, error) {
	return nil, nil
}

func (builder *testCurrentClusterProfileBuilder) BuildPackageImages(string, string, string) ([]v1.ImageRef, error) {
	return nil, nil
}

func (builder *testCurrentClusterProfileBuilder) PackageAccelerators(string) []string {
	return nil
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

func TestNewBuilderUsesReleaseProfileBuilder(t *testing.T) {
	builder := NewBuilder()

	if builder.releaseProfileBuilder == nil {
		t.Fatal("expected release profile builder")
	}
}

func TestNewAppUsesReleaseProfileBuilder(t *testing.T) {
	application := NewApp(&config.CoreConfig{}, map[string]controllers.Controller{})

	if application.releaseProfileBuilder == nil {
		t.Fatal("expected release profile builder")
	}
}

func TestBuilderBuildPreservesReleaseProfileBuilder(t *testing.T) {
	customBuilder := &testReleaseInfoBuilder{}
	builder := NewBuilder().WithConfig(&config.CoreConfig{GinEngine: gin.New()})
	builder.controllerInits = map[string]ControllerFactory{}
	builder.WithReleaseProfileBuilder(customBuilder)

	application, err := builder.Build()
	if err != nil {
		t.Fatalf("build application: %v", err)
	}

	if application.releaseProfileBuilder != customBuilder {
		t.Fatalf("expected built app to retain custom release profile builder, got %T", application.releaseProfileBuilder)
	}
}

func TestBuilderWithNilReleaseProfileBuilderUsesDefault(t *testing.T) {
	builder := NewBuilder().WithReleaseProfileBuilder(nil)

	if builder.releaseProfileBuilder == nil {
		t.Fatal("expected default release profile builder after nil input")
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

func TestAppRunStopsWhenCurrentReleaseInfoSynchronizationFails(t *testing.T) {
	store := storagemocks.NewMockStorage(t)
	store.On("ListReleaseInfo").Return([]v1.ReleaseInfo{{Metadata: &v1.Metadata{Name: "v1.2.0"}}}, nil).Once()
	application := NewApp(&config.CoreConfig{Storage: store, Version: "v1.2.0"}, map[string]controllers.Controller{})
	syncErr := errors.New("database unavailable")

	var gotBaseline string
	application.synchronizeCurrentBaseline = func(
		_ releaseprofile.CurrentBaselineStore,
		baseline string,
		_ releaseprofile.Builder,
	) error {
		gotBaseline = baseline
		return syncErr
	}

	err := application.Run(context.Background())

	if !errors.Is(err, syncErr) {
		t.Fatal("expected synchronization error")
	}
	if gotBaseline != "v1.2.0" {
		t.Fatalf("expected v1.2.0 baseline, got %q", gotBaseline)
	}
	store.AssertExpectations(t)
}

func TestAppRunResolvesCurrentControlPlaneBaselineBeforeSynchronization(t *testing.T) {
	tests := []struct {
		name           string
		identity       string
		infos          []v1.ReleaseInfo
		releaseBuilder releaseprofile.Builder
		want           string
		wantSync       bool
	}{
		{
			name:     "development uses highest persisted stable baseline",
			identity: "dev",
			infos: []v1.ReleaseInfo{
				{Metadata: &v1.Metadata{Name: "v1.1.0"}},
				{Metadata: &v1.Metadata{Name: "v1.2.0"}},
			},
			want:     "v1.2.0",
			wantSync: false,
		},
		{
			name:     "dirty uses highest persisted stable baseline",
			identity: "v1.2.0-dirty",
			infos: []v1.ReleaseInfo{
				{Metadata: &v1.Metadata{Name: "v1.2.0"}},
				{Metadata: &v1.Metadata{Name: "v1.3.0"}},
			},
			want:     "v1.3.0",
			wantSync: false,
		},
		{
			name:     "workflow commit build uses highest persisted stable baseline",
			identity: "c636802",
			infos: []v1.ReleaseInfo{
				{Metadata: &v1.Metadata{Name: "v1.1.1"}},
				{Metadata: &v1.Metadata{Name: "v1.2.0"}},
			},
			want:     "v1.2.0",
			wantSync: false,
		},
		{
			name:     "workflow commit bootstrap uses current builder baseline",
			identity: "b64e294",
			want:     "v1.2.0",
			wantSync: true,
		},
		{
			name:           "workflow commit bootstrap uses injected builder baseline",
			identity:       "b64e294",
			releaseBuilder: &testReleaseInfoBuilderWithBaseline{baseline: "v1.3.0"},
			want:           "v1.3.0",
			wantSync:       true,
		},
		{
			name:     "nightly preserves exact release identity",
			identity: "v1.2.0-nightly.20260805",
			infos:    []v1.ReleaseInfo{{Metadata: &v1.Metadata{Name: "v1.2.0"}}},
			want:     "v1.2.0-nightly.20260805",
			wantSync: true,
		},
		{
			name:     "release candidate preserves exact release identity",
			identity: "v1.2.0-rc.1",
			infos:    []v1.ReleaseInfo{{Metadata: &v1.Metadata{Name: "v1.2.0"}}},
			want:     "v1.2.0-rc.1",
			wantSync: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := storagemocks.NewMockStorage(t)
			store.On("ListReleaseInfo").Return(tt.infos, nil).Once()
			application := NewApp(&config.CoreConfig{Storage: store, Version: tt.identity}, map[string]controllers.Controller{})
			if tt.releaseBuilder != nil {
				application.releaseProfileBuilder = tt.releaseBuilder
			}
			resolution, err := application.currentControlPlaneBaseline()
			if err != nil {
				t.Fatalf("resolve baseline: %v", err)
			}
			if resolution.name != tt.want {
				t.Fatalf("expected baseline %q, got %q", tt.want, resolution.name)
			}
			if resolution.shouldSynchronize != tt.wantSync {
				t.Fatalf("expected synchronize=%t, got %t", tt.wantSync, resolution.shouldSynchronize)
			}
			store.AssertExpectations(t)
		})
	}
}

func TestBuilderRunPassesInjectedReleaseProfileBuilderToSynchronization(t *testing.T) {
	store := storagemocks.NewMockStorage(t)
	store.On("ListReleaseInfo").Return([]v1.ReleaseInfo{{Metadata: &v1.Metadata{Name: "v1.2.0"}}}, nil).Once()
	releaseBuilder := &testReleaseInfoBuilder{}
	builder := NewBuilder().WithConfig(&config.CoreConfig{GinEngine: gin.New(), Storage: store, Version: "v1.2.0"})
	builder.controllerInits = map[string]ControllerFactory{}
	builder.WithReleaseProfileBuilder(releaseBuilder)

	application, err := builder.Build()
	if err != nil {
		t.Fatalf("build application: %v", err)
	}

	syncErr := errors.New("stop after synchronization")
	application.synchronizeCurrentBaseline = func(
		_ releaseprofile.CurrentBaselineStore,
		_ string,
		gotBuilder releaseprofile.Builder,
	) error {
		if gotBuilder != releaseBuilder {
			t.Fatalf("expected injected release profile builder, got %T", gotBuilder)
		}

		return syncErr
	}

	err = application.Run(context.Background())
	if !errors.Is(err, syncErr) {
		t.Fatalf("expected synchronization error, got %v", err)
	}
	store.AssertExpectations(t)
}
