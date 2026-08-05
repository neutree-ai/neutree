package app

import (
	"errors"
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
