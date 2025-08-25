package app

import (
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/neutree-ai/neutree/cmd/neutree-api/app/config"
)

func TestNewBuilder(t *testing.T) {
	builder := NewBuilder()
	if builder == nil {
		t.Fatal("Expected NewBuilder to return a non-nil Builder")
	}

	if len(builder.routeInits) == 0 {
		t.Error("Expected NewBuilder to register default routeInits")
	}
	if len(builder.middlewareInits) == 0 {
		t.Error("Expected NewBuilder to register default middlewareInits")
	}
	if len(builder.routesToMiddlewares) == 0 {
		t.Error("Expected NewBuilder to register default routesToMiddlewares")
	}
}

func TestBuilderWithConfig(t *testing.T) {
	builder := NewBuilder()
	config := &config.APIConfig{}
	builder.WithConfig(config)

	if builder.config != config {
		t.Errorf("Expected config to be set in builder, got %v", builder.config)
	}
}

func TestBuilderWithRoute(t *testing.T) {
	builder := NewBuilder()
	routeFactory := func(opts *RouteOptions) {}
	builder.WithRoute("test-route", routeFactory)

	if _, exists := builder.routeInits["test-route"]; !exists {
		t.Error("Expected route 'test-route' to be registered in builder")
	}
}

func TestBuilderWithGlobalMiddleware(t *testing.T) {
	builder := NewBuilder()
	middlewareFactory := func(opts *MiddlewareOptions) gin.HandlerFunc {
		return func(c *gin.Context) {}
	}

	builder.WithGlobalMiddleware(middlewareFactory)

	if len(builder.globalMiddlewaresInits) == 0 {
		t.Error("Expected global middlewares to be registered in builder")
	}
}

func TestBuilderWithMiddleware(t *testing.T) {
	builder := NewBuilder()
	middlewareFactory := func(opts *MiddlewareOptions) gin.HandlerFunc {
		return func(c *gin.Context) {}
	}

	builder.WithMiddleware("test-middleware", middlewareFactory, []string{"test-route"})

	if _, exists := builder.middlewareInits["test-middleware"]; !exists {
		t.Error("Expected middleware 'test-middleware' to be registered in builder")
	}
	if len(builder.routesToMiddlewares["test-route"]) == 0 {
		t.Error("Expected 'test-route' to have registered middlewares")
	}
	if builder.routesToMiddlewares["test-route"][0] != "test-middleware" {
		t.Errorf("Expected 'test-route' to have 'test-middleware', got %v", builder.routesToMiddlewares["test-route"])
	}

	builder.WithMiddleware("test-middleware", middlewareFactory, []string{"test-route-1"})

	if _, exists := builder.routesToMiddlewares["test-route-1"]; !exists {
		t.Error("Expected 'test-route-1' to have registered middlewares")
	}
	if len(builder.routesToMiddlewares["test-route-1"]) == 0 {
		t.Error("Expected 'test-route-1' to have registered middlewares")
	}
	if builder.routesToMiddlewares["test-route-1"][0] != "test-middleware" {
		t.Errorf("Expected 'test-route-1' to have 'test-middleware', got %v", builder.routesToMiddlewares["test-route-1"])
	}

	builder.WithMiddleware("test-middleware", middlewareFactory, []string{"test-route-1"})
	if len(builder.routesToMiddlewares["test-route-1"]) != 1 {
		t.Error("Expected 'test-route-1' to have only one middleware registered")
	}
}
