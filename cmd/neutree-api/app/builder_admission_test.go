package app

import (
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/cmd/neutree-api/app/config"
	"github.com/neutree-ai/neutree/internal/routes/proxies"
	"github.com/neutree-ai/neutree/pkg/admission"
)

func TestBuilderAdmissionConfigurersRunAfterRoutesAndSealRegistry(t *testing.T) {
	builder := newAdmissionTestBuilder()
	var events []string
	var routeRegistry, configuredRegistry *admission.Registry
	builder.WithRoute("test", func(options *RouteOptions) error {
		events = append(events, "route")
		routeRegistry = options.Admission
		return nil
	})
	builder.WithAdmissionConfigurer("first", func(options *AdmissionOptions) error {
		events = append(events, "first")
		configuredRegistry = options.Registry
		return nil
	})
	builder.WithAdmissionConfigurer("second", func(options *AdmissionOptions) error {
		events = append(events, "second")
		return nil
	})

	_, err := builder.Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if !reflect.DeepEqual(events, []string{"route", "first", "second"}) {
		t.Errorf("configurer order = %v, want [route first second]", events)
	}
	if routeRegistry != configuredRegistry {
		t.Error("route and configurer did not receive the shared admission registry")
	}
	if err := configuredRegistry.RegisterResource("after-build"); err == nil || !strings.Contains(err.Error(), "sealed") {
		t.Errorf("registry accepted registration after Build(), error = %v", err)
	}
}

func TestBuilderAdmissionConfigurersRejectDuplicateNames(t *testing.T) {
	builder := newAdmissionTestBuilder()
	builder.WithAdmissionConfigurer("duplicate", func(*AdmissionOptions) error { return nil })
	builder.WithAdmissionConfigurer("duplicate", func(*AdmissionOptions) error { return nil })

	_, err := builder.Build()
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("Build() error = %v, want duplicate configurer error", err)
	}
}

func TestBuilderAdmissionConfigurerErrorAbortsBuild(t *testing.T) {
	builder := newAdmissionTestBuilder()
	want := errors.New("configure admission")
	builder.WithAdmissionConfigurer("failing", func(*AdmissionOptions) error { return want })

	_, err := builder.Build()
	if !errors.Is(err, want) {
		t.Errorf("Build() error = %v, want %v", err, want)
	}
}

func TestBuilderAdmissionNilConfigurerFailsWithoutPanic(t *testing.T) {
	builder := newAdmissionTestBuilder()
	builder.WithAdmissionConfigurer("nil", nil)

	err := buildAdmissionWithoutPanic(t, builder)
	if err == nil || !strings.Contains(err.Error(), "nil") {
		t.Errorf("Build() error = %v, want nil configurer error", err)
	}
}

func TestBuilderAdmissionConfigurerFailurePreventsBuildReuse(t *testing.T) {
	builder := newAdmissionTestBuilder()
	want := errors.New("configure admission")
	builder.WithAdmissionConfigurer("failing", func(*AdmissionOptions) error { return want })

	if err := buildAdmissionWithoutPanic(t, builder); !errors.Is(err, want) {
		t.Errorf("first Build() error = %v, want %v", err, want)
	}
	if err := buildAdmissionWithoutPanic(t, builder); err == nil || !strings.Contains(err.Error(), "already") {
		t.Errorf("second Build() error = %v, want already attempted error", err)
	}
}

func TestBuilderAdmissionMissingConfigAllowsLaterBuild(t *testing.T) {
	builder := newAdmissionTestBuilder()
	builder.config = nil

	if err := buildAdmissionWithoutPanic(t, builder); err == nil || !strings.Contains(err.Error(), "config") {
		t.Errorf("first Build() error = %v, want missing config error", err)
	}
	builder.WithConfig(&config.APIConfig{
		GinEngine:    gin.New(),
		StaticConfig: &config.StaticConfig{},
	})
	if err := buildAdmissionWithoutPanic(t, builder); err != nil {
		t.Errorf("second Build() error = %v, want success", err)
	}
}

func TestBuilderPassesAdmissionRegistryToProxyRoutes(t *testing.T) {
	builder := newAdmissionTestBuilder()
	var proxyRegistry, configuredRegistry *admission.Registry
	builder.WithAdmissionConfigurer("capture", func(options *AdmissionOptions) error {
		configuredRegistry = options.Registry
		return nil
	})
	builder.WithRoute("proxy", ProxiesRouteFactory(func(_ *gin.RouterGroup, _ []gin.HandlerFunc, deps *proxies.Dependencies) {
		proxyRegistry = deps.Admission
	}))

	if _, err := builder.Build(); err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if proxyRegistry != configuredRegistry {
		t.Error("proxy route did not receive the shared admission registry")
	}
}

func TestProxiesRouteFactoryWithErrorAbortsBuildOnAdmissionRegistrationFailure(t *testing.T) {
	builder := newAdmissionTestBuilder()
	register := func(_ *gin.RouterGroup, _ []gin.HandlerFunc, deps *proxies.Dependencies) error {
		return deps.Admission.RegisterResource(admission.NewResource[v1.Engine]("engines"))
	}
	builder.WithRoute("first", ProxiesRouteFactoryWithError(register))
	builder.WithRoute("duplicate", ProxiesRouteFactoryWithError(register))

	err := buildAdmissionWithoutPanic(t, builder)
	if err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Errorf("Build() error = %v, want admission registration failure", err)
	}
}

func TestBuilderAdmissionConfigurerRegistersOrderedEnterpriseHooks(t *testing.T) {
	builder := newAdmissionTestBuilder()
	var registry *admission.Registry
	builder.WithAdmissionConfigurer("enterprise.admission", func(options *AdmissionOptions) error {
		registry = options.Registry
		resource := admission.NewResource[v1.Engine]("enterprise-test")
		if err := options.Registry.RegisterHook(resource, admission.ValidateCreate(
			admission.HookMeta{Name: "enterprise.second", Order: 1001}, 91802,
			func(admission.RequestContext, v1.Engine) error { return nil },
		)); err != nil {
			return err
		}
		return options.Registry.RegisterHook(resource, admission.ValidateCreate(
			admission.HookMeta{Name: "enterprise.first", Order: 1000}, 91801,
			func(admission.RequestContext, v1.Engine) error { return nil },
		))
	})

	_, err := builder.Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	chain, err := registry.Chain(admission.NewResource[v1.Engine]("enterprise-test"), admission.Create)
	if err != nil {
		t.Fatalf("Chain() error = %v", err)
	}
	if got := chain.Hooks(); !reflect.DeepEqual(got, []admission.HookMeta{
		{Name: "enterprise.first", Operation: admission.Create, Phase: admission.Validating, Order: 1000},
		{Name: "enterprise.second", Operation: admission.Create, Phase: admission.Validating, Order: 1001},
	}) {
		t.Errorf("enterprise hook order = %v", got)
	}
	if err := registry.RegisterHook(admission.NewResource[v1.Engine]("enterprise-test"), admission.ValidateCreate(
		admission.HookMeta{Name: "enterprise.after-seal", Order: 1002}, 91803,
		func(admission.RequestContext, v1.Engine) error { return nil },
	)); err == nil || !strings.Contains(err.Error(), "sealed") {
		t.Errorf("RegisterHook() after Build() error = %v, want sealed error", err)
	}
}

func TestBuilderAdmissionConfigurerRejectsEnterpriseHookOutsideReservedOrderBand(t *testing.T) {
	builder := newAdmissionTestBuilder()
	builder.WithAdmissionConfigurer("enterprise.admission", func(options *AdmissionOptions) error {
		return options.Registry.RegisterHook(admission.NewResource[v1.Engine]("enterprise-test"), admission.ValidateCreate(
			admission.HookMeta{Name: "enterprise.invalid-order", Order: 999}, 91804,
			func(admission.RequestContext, v1.Engine) error { return nil },
		))
	})

	err := buildAdmissionWithoutPanic(t, builder)
	if err == nil || !strings.Contains(err.Error(), "outside 1000-1999") {
		t.Errorf("Build() error = %v, want enterprise order-band error", err)
	}
}

func TestBuilderAdmissionConfigurerAppendsEnterpriseHooksAfterDefaultCommunityHooks(t *testing.T) {
	builder := NewBuilder().WithConfig(&config.APIConfig{
		GinEngine:    gin.New(),
		StaticConfig: &config.StaticConfig{},
	})
	var registry *admission.Registry
	builder.WithAdmissionConfigurer("enterprise.admission", func(options *AdmissionOptions) error {
		registry = options.Registry
		return options.Registry.RegisterHook(admission.NewResource[v1.Endpoint]("endpoints"), admission.ValidateCreate(
			admission.HookMeta{Name: "enterprise.endpoint.create", Order: 1000}, 91805,
			func(admission.RequestContext, v1.Endpoint) error { return nil },
		))
	})

	if _, err := builder.Build(); err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	chain, err := registry.Chain(admission.NewResource[v1.Endpoint]("endpoints"), admission.Create)
	if err != nil {
		t.Fatalf("Chain() error = %v", err)
	}
	hooks := chain.Hooks()
	if len(hooks) < 2 {
		t.Fatalf("endpoint create hooks = %v, want default community and enterprise hooks", hooks)
	}
	enterpriseIndex := -1
	for index, hook := range hooks {
		if hook.Name == "enterprise.endpoint.create" {
			enterpriseIndex = index
			if hook.Order != 1000 {
				t.Errorf("enterprise hook order = %d, want 1000", hook.Order)
			}
			continue
		}
		if enterpriseIndex >= 0 {
			t.Errorf("community hook %q ran after enterprise hook", hook.Name)
		}
	}
	if enterpriseIndex == -1 {
		t.Error("enterprise hook was not registered")
	}
}

func TestAdmissionOptionsExposeOnlyRegistry(t *testing.T) {
	optionsType := reflect.TypeFor[AdmissionOptions]()
	if optionsType.NumField() != 1 {
		t.Fatalf("AdmissionOptions exposes %d fields, want only Registry", optionsType.NumField())
	}
	field := optionsType.Field(0)
	if field.Name != "Registry" || field.Type != reflect.TypeFor[*admission.Registry]() {
		t.Errorf("AdmissionOptions field = %s %s, want Registry *admission.Registry", field.Name, field.Type)
	}
}

func TestDefaultBuilderAdmissionCoverageMatchesMountedResourceWrites(t *testing.T) {
	builder := NewBuilder().WithConfig(&config.APIConfig{
		GinEngine:    gin.New(),
		StaticConfig: &config.StaticConfig{},
	})
	var registry *admission.Registry
	builder.WithAdmissionConfigurer("coverage", func(options *AdmissionOptions) error {
		registry = options.Registry
		return nil
	})

	if _, err := builder.Build(); err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	expected := map[string]map[string]admission.Operation{
		"/api/v1/api_keys":           {http.MethodPatch: admission.Update},
		"/api/v1/workspaces":         {http.MethodPost: admission.Create, http.MethodPatch: admission.Update},
		"/api/v1/roles":              {http.MethodPost: admission.Create, http.MethodPatch: admission.Update},
		"/api/v1/role_assignments":   {http.MethodPost: admission.Create, http.MethodPatch: admission.Update},
		"/api/v1/user_profiles":      {http.MethodPost: admission.Create, http.MethodPatch: admission.Update},
		"/api/v1/clusters":           {http.MethodPost: admission.Create, http.MethodPatch: admission.Update},
		"/api/v1/image_registries":   {http.MethodPost: admission.Create, http.MethodPatch: admission.Update},
		"/api/v1/model_registries":   {http.MethodPost: admission.Create, http.MethodPatch: admission.Update},
		"/api/v1/endpoints":          {http.MethodPost: admission.Create, http.MethodPatch: admission.Update},
		"/api/v1/engines":            {http.MethodPost: admission.Create, http.MethodPatch: admission.Update},
		"/api/v1/model_catalogs":     {http.MethodPost: admission.Create, http.MethodPatch: admission.Update},
		"/api/v1/oem_configs":        {http.MethodPost: admission.Create, http.MethodPatch: admission.Update},
		"/api/v1/external_endpoints": {http.MethodPost: admission.Create, http.MethodPatch: admission.Update},
	}
	found := make(map[string]map[string]struct{}, len(expected))
	for _, route := range builder.config.GinEngine.Routes() {
		if route.Method != http.MethodPost && route.Method != http.MethodPatch {
			continue
		}
		if !strings.Contains(route.Handler, "internal/routes/proxies.CreateStructProxyHandler") {
			continue
		}
		operations, isResourceWrite := expected[route.Path]
		if !isResourceWrite {
			t.Errorf("uncovered default REST resource write %s %s", route.Method, route.Path)
			continue
		}
		operation, expectedMethod := operations[route.Method]
		if !expectedMethod {
			t.Errorf("unexpected resource write route %s %s", route.Method, route.Path)
			continue
		}
		if _, err := registry.Chain(strings.TrimPrefix(route.Path, "/api/v1/"), operation); err != nil {
			t.Errorf("resource write %s %s has no admission descriptor: %v", route.Method, route.Path, err)
		}
		if found[route.Path] == nil {
			found[route.Path] = make(map[string]struct{})
		}
		found[route.Path][route.Method] = struct{}{}
	}
	for path, operations := range expected {
		for method := range operations {
			if _, ok := found[path][method]; !ok {
				t.Errorf("default resource write %s %s is not mounted", method, path)
			}
		}
	}
	if _, err := registry.Chain("unregistered-resource", admission.Create); err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Errorf("unregistered write Chain() error = %v, want not registered", err)
	}
	if err := registry.RegisterResource("after-build"); err == nil || !strings.Contains(err.Error(), "sealed") {
		t.Errorf("registry accepted registration after default Build(), error = %v", err)
	}
}

func newAdmissionTestBuilder() *Builder {
	builder := NewBuilder()
	builder.routeInits = make(map[string]RouteFactory)
	builder.middlewareInits = make(map[string]MiddlewareFactory)
	builder.routesToMiddlewares = make(map[string][]string)
	builder.WithConfig(&config.APIConfig{
		GinEngine:    gin.New(),
		StaticConfig: &config.StaticConfig{},
	})
	return builder
}

func buildAdmissionWithoutPanic(t *testing.T, builder *Builder) (err error) {
	t.Helper()
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("Build() panicked: %v", recovered)
		}
	}()
	_, err = builder.Build()
	return err
}
