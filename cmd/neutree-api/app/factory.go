package app

import (
	"github.com/gin-gonic/gin"
	"github.com/supabase-community/gotrue-go"

	"github.com/neutree-ai/neutree/cmd/neutree-api/app/config"
	"github.com/neutree-ai/neutree/internal/middleware"
	"github.com/neutree-ai/neutree/internal/routes/auth"
	"github.com/neutree-ai/neutree/internal/routes/credentials"
	"github.com/neutree-ai/neutree/internal/routes/models"
	"github.com/neutree-ai/neutree/internal/routes/proxies"
	"github.com/neutree-ai/neutree/internal/routes/system"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type Middleware gin.HandlerFunc

type RouteFactory func(deps *RouteOptions) error

type RouteOptions struct {
	Config      *config.APIConfig
	Group       *gin.RouterGroup
	Middlewares []gin.HandlerFunc
}

type ModelRegisterFunc func(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *models.Dependencies)

func ModelsRouteFactory(register ModelRegisterFunc) RouteFactory {
	return func(deps *RouteOptions) error {
		register(deps.Group, deps.Middlewares, &models.Dependencies{
			Storage:    deps.Config.Storage,
			AuthConfig: deps.Config.AuthConfig,
		})

		return nil
	}
}

type ProxyRegisterFunc func(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *proxies.Dependencies)

func ProxiesRouteFactory(register ProxyRegisterFunc) RouteFactory {
	return func(deps *RouteOptions) error {
		register(deps.Group, deps.Middlewares, &proxies.Dependencies{
			Storage:          deps.Config.Storage,
			StorageAccessURL: deps.Config.StorageAccessURL,
			AuthEndpoint:     deps.Config.AuthEndpoint,
			AuthConfig:       deps.Config.AuthConfig,
		})

		return nil
	}
}

type SystemRegisterFunc func(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *system.Dependencies)

func SystemRouteFactory(register SystemRegisterFunc) RouteFactory {
	return func(deps *RouteOptions) error {
		register(deps.Group, deps.Middlewares, &system.Dependencies{
			GrafanaURL: deps.Config.GrafanaURL,
			Version:    deps.Config.Version,
			AuthConfig: deps.Config.AuthConfig,
		})

		return nil
	}
}

type AuthRegisterFunc func(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *auth.Dependencies)

func AuthRouteFactory(register AuthRegisterFunc) RouteFactory {
	return func(deps *RouteOptions) error {
		jwtToken, err := storage.CreateServiceToken(deps.Config.AuthConfig.JwtSecret)
		if err != nil {
			return err
		}

		authClient := gotrue.New("", "").WithCustomGoTrueURL(deps.Config.AuthEndpoint).WithToken(*jwtToken)

		register(deps.Group, deps.Middlewares, &auth.Dependencies{
			AuthEndpoint: deps.Config.AuthEndpoint,
			AuthConfig:   deps.Config.AuthConfig,
			Storage:      deps.Config.Storage,
			AuthClient:   authClient,
		})

		return nil
	}
}

type CredentialsRegisterFunc func(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *credentials.Dependencies)

func CredentialsRouteFactory(register CredentialsRegisterFunc) RouteFactory {
	return func(deps *RouteOptions) error {
		register(deps.Group, deps.Middlewares, &credentials.Dependencies{
			Storage:          deps.Config.Storage,
			StorageAccessURL: deps.Config.StorageAccessURL,
		})

		return nil
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
