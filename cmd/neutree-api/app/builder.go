package app

import (
	"fmt"

	"github.com/gin-contrib/static"
	"github.com/gin-gonic/gin"
	"k8s.io/klog"

	"github.com/neutree-ai/neutree/cmd/neutree-api/app/config"
	"github.com/neutree-ai/neutree/internal/middleware"
	"github.com/neutree-ai/neutree/internal/routes/auth"
	"github.com/neutree-ai/neutree/internal/routes/credentials"
	"github.com/neutree-ai/neutree/internal/routes/models"
	"github.com/neutree-ai/neutree/internal/routes/proxies"
	"github.com/neutree-ai/neutree/internal/routes/system"
)

// Builder is the API application builder
type Builder struct {
	routeInits             map[string]RouteFactory
	middlewareInits        map[string]MiddlewareFactory
	routesToMiddlewares    map[string][]string
	config                 *config.APIConfig
	globalMiddlewaresInits []MiddlewareFactory
}

// NewBuilder creates a new API builder
func NewBuilder() *Builder {
	b := &Builder{
		middlewareInits:        make(map[string]MiddlewareFactory),
		routeInits:             make(map[string]RouteFactory),
		routesToMiddlewares:    make(map[string][]string),
		globalMiddlewaresInits: []MiddlewareFactory{},
	}

	// Register default route handlers
	defaultRouteInits := map[string]RouteFactory{
		"models":          ModelsRouteFactory(models.RegisterModelsRoutes),
		"serve-proxy":     ProxiesRouteFactory(proxies.RegisterRayServeProxyRoutes),
		"dashboard-proxy": ProxiesRouteFactory(proxies.RegisterRayDashboardProxyRoutes),
		"system":          SystemRouteFactory(system.RegisterSystemRoutes),
		// Auth route (no auth required for authentication itself)
		"auth": AuthRouteFactory(auth.RegisterAuthRoutes),
		// Credentials route is used for get resource with sensitive data
		"credentials": CredentialsRouteFactory(credentials.RegisterCredentialsRoutes),
		// PostgREST proxy routes
		// Auth middleware is applied to:
		// - Validate and pass-through JWT tokens to PostgREST
		// - Convert API keys (sk_*) to PostgREST-compatible JWT tokens
		"rest/api-keys":         ProxiesRouteFactory(proxies.RegisterAPIKeyRoutes),
		"rest/workspaces":       ProxiesRouteFactory(proxies.RegisterWorkspaceRoutes),
		"rest/roles":            ProxiesRouteFactory(proxies.RegisterRoleRoutes),
		"rest/role-assignments": ProxiesRouteFactory(proxies.RegisterRoleAssignmentRoutes),
		"rest/user-profiles":    ProxiesRouteFactory(proxies.RegisterUserProfileRoutes),
		"rest/clusters":         ProxiesRouteFactory(proxies.RegisterClusterRoutes),
		"rest/image-registries": ProxiesRouteFactory(proxies.RegisterImageRegistryRoutes),
		"rest/model-registries": ProxiesRouteFactory(proxies.RegisterModelRegistryRoutes),
		"rest/endpoints":        ProxiesRouteFactory(proxies.RegisterEndpointRoutes),
		"rest/engines":          ProxiesRouteFactory(proxies.RegisterEngineRoutes),
		"rest/model-catalogs":   ProxiesRouteFactory(proxies.RegisterModelCatalogRoutes),
		"rest/oem-configs":      ProxiesRouteFactory(proxies.RegisterOEMConfigRoutes),
		"rest/rpc":              ProxiesRouteFactory(proxies.RegisterPostgrestRPCProxyRoutes),
	}

	for name, routeInit := range defaultRouteInits {
		b.routeInits[name] = routeInit
	}

	// Register default middleware handlers
	defaultMiddlewareInits := map[string]MiddlewareFactory{
		"auth": CommonMiddlewareFactory(middleware.Auth),
	}

	for name, middlewareInit := range defaultMiddlewareInits {
		b.middlewareInits[name] = middlewareInit
	}
	// Register default middlewares to routes
	defaultRoutesToMiddlewares := map[string][]string{
		"models":      {"auth"},
		"serve-proxy": {"auth"},
		"system":      {"auth"},
		// PostgREST proxy routes now require auth middleware to:
		// 1. Validate JWT tokens (pass-through to PostgREST)
		// 2. Convert API keys to PostgREST-compatible JWT tokens
		"rest/api-keys":         {"auth"},
		"rest/workspaces":       {"auth"},
		"rest/roles":            {"auth"},
		"rest/role-assignments": {"auth"},
		"rest/user-profiles":    {"auth"},
		"rest/clusters":         {"auth"},
		"rest/image-registries": {"auth"},
		"rest/model-registries": {"auth"},
		"rest/endpoints":        {"auth"},
		"rest/engines":          {"auth"},
		"rest/model-catalogs":   {"auth"},
		"rest/oem-configs":      {"auth"},
		"rest/rpc":              {"auth"},
		"credentials":           {"auth"},
	}

	for route, middlewares := range defaultRoutesToMiddlewares {
		b.routesToMiddlewares[route] = middlewares
	}

	return b
}

// WithConfig sets the configuration for the builder
func (b *Builder) WithConfig(c *config.APIConfig) *Builder {
	b.config = c
	return b
}

// WithRoute registers a route
func (b *Builder) WithRoute(name string, routeInit RouteFactory) *Builder {
	klog.Info("Registering route:", name)

	b.routeInits[name] = routeInit

	return b
}

// WithMiddleware registers a middleware to routes
func (b *Builder) WithMiddleware(name string, middlewareInit MiddlewareFactory, routes []string) *Builder {
	if _, exists := b.middlewareInits[name]; !exists {
		b.middlewareInits[name] = middlewareInit
	}

	for _, route := range routes {
		if _, exists := b.routesToMiddlewares[route]; !exists {
			b.routesToMiddlewares[route] = []string{}
		}

		exists := false

		for _, mwName := range b.routesToMiddlewares[route] {
			if mwName == name {
				// Middleware already registered for this route
				exists = true
				break
			}
		}

		if !exists {
			b.routesToMiddlewares[route] = append(b.routesToMiddlewares[route], name)
		}
	}

	return b
}

// WithGlobalMiddleware adds a global middleware that applies to all routes
func (b *Builder) WithGlobalMiddleware(middlewareInit MiddlewareFactory) *Builder {
	b.globalMiddlewaresInits = append(b.globalMiddlewaresInits, middlewareInit)
	return b
}

// Build creates and initializes all components
func (b *Builder) Build() (*App, error) {
	if b.config == nil {
		return nil, fmt.Errorf("config is required")
	}

	middlewareOptions := &MiddlewareOptions{
		Config: b.config,
	}

	// Apply global middlewares to the gin engine
	for _, mw := range b.globalMiddlewaresInits {
		b.config.GinEngine.Use(mw(middlewareOptions))
	}

	middlewareHandleMap := make(map[string]gin.HandlerFunc)
	// initialize middleware handlers
	for name, factory := range b.middlewareInits {
		middlewareHandleMap[name] = factory(middlewareOptions)
	}

	// register static file serving middleware
	b.config.GinEngine.Use(static.Serve("/", static.LocalFile(b.config.StaticConfig.Dir, true)))

	// register health check endpoint
	b.config.GinEngine.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	apiV1 := b.config.GinEngine.RouterGroup.Group("/api/v1")

	// Initialize route handlers
	for name, factory := range b.routeInits {
		middlewares := []gin.HandlerFunc{}

		if routeMiddlewares, exists := b.routesToMiddlewares[name]; exists {
			for _, mwName := range routeMiddlewares {
				if mw, exists := middlewareHandleMap[mwName]; exists {
					middlewares = append(middlewares, mw)
				} else {
					return nil, fmt.Errorf("middleware %s not found for route %s", mwName, name)
				}
			}
		}

		opts := &RouteOptions{
			Config:      b.config,
			Group:       apiV1,
			Middlewares: middlewares,
		}

		klog.Info("Initializing route:", name)

		err := factory(opts)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize route %s: %v", name, err)
		}
	}

	return NewApp(b.config), nil
}
