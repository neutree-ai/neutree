package app

import (
	"github.com/gin-gonic/gin"

	"github.com/neutree-ai/neutree/cmd/neutree-api/app/config"
	"github.com/neutree-ai/neutree/internal/middleware"
	"github.com/neutree-ai/neutree/internal/routes/models"
	"github.com/neutree-ai/neutree/internal/routes/proxies"
	"github.com/neutree-ai/neutree/internal/routes/system"
)

type Middleware gin.HandlerFunc

type RouteFactory func(deps *RouteOptions)

type RouteOptions struct {
	Config      *config.APIConfig
	Group       *gin.RouterGroup
	Middlewares []gin.HandlerFunc
}

type ModelRegisterFunc func(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *models.Dependencies)

func ModelsRouteFactory(register ModelRegisterFunc) RouteFactory {
	return func(deps *RouteOptions) {
		register(deps.Group, deps.Middlewares, &models.Dependencies{
			Storage:    deps.Config.Storage,
			AuthConfig: deps.Config.AuthConfig,
		})
	}
}

type ProxyRegisterFunc func(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *proxies.Dependencies)

func ProxiesRouteFactory(register ProxyRegisterFunc) RouteFactory {
	return func(deps *RouteOptions) {
		register(deps.Group, deps.Middlewares, &proxies.Dependencies{
			Storage:          deps.Config.Storage,
			StorageAccessURL: deps.Config.StorageAccessURL,
			AuthEndpoint:     deps.Config.AuthEndpoint,
			AuthConfig:       deps.Config.AuthConfig,
		})
	}
}

type SystemRegisterFunc func(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *system.Dependencies)

func SystemRouteFactory(register SystemRegisterFunc) RouteFactory {
	return func(deps *RouteOptions) {
		register(deps.Group, deps.Middlewares, &system.Dependencies{
			GrafanaURL: deps.Config.GrafanaURL,
			Version:    deps.Config.Version,
			AuthConfig: deps.Config.AuthConfig,
		})
	}
}

type MiddlewareOptions struct {
	Config *config.APIConfig
}

type MiddlewareRegisterFunc func(deps middleware.Dependencies) gin.HandlerFunc

type MiddlewareFactory func(deps *MiddlewareOptions) gin.HandlerFunc

func CommonMiddlewareFactory(register MiddlewareRegisterFunc) MiddlewareFactory {
	return func(deps *MiddlewareOptions) gin.HandlerFunc {
		return register(middleware.Dependencies{
			Config: deps.Config.AuthConfig,
		})
	}
}
