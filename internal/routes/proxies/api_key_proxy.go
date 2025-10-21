package proxies

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func getAPIKeyProxyConfig() *ResourceProxyConfig {
	return &ResourceProxyConfig{
		ResourceName: "api_keys",
		TableName:    "api_keys",
		Methods: map[string]*MethodConfig{
			http.MethodGet: {
				Enabled: true,
				FieldSelector: &FieldSelector{
					ExcludeFields: map[string]struct{}{
						"status.sk_value": {},
					},
				},
			},
			http.MethodPatch: {
				Enabled: true,
				FieldSelector: &FieldSelector{
					ExcludeFields: map[string]struct{}{
						"status.sk_value": {},
					},
				},
			},
			http.MethodPost: {
				Enabled: false,
			},
			http.MethodPut: {
				Enabled: false,
			},
			http.MethodDelete: {
				Enabled: false,
			},
		},
	}
}

func RegisterAPIKeyRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) {
	config := getAPIKeyProxyConfig()

	proxyGroup := group.Group("/" + config.ResourceName)
	proxyGroup.Use(middlewares...)
	proxyGroup.Any("", CreateResourceProxyHandler(deps, config))
}
