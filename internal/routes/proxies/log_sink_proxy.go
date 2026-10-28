package proxies

import (
	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// RegisterLogSinkRoutes registers routes for the global LogSink resource.
// Auth secrets (api:"-") are masked on reads and backfilled on PATCH; they are
// readable only through the credentials API.
//
// POST /log_sinks/test_connection probes a sink on demand; see
// handleLogSinkTestConnection.
func RegisterLogSinkRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) {
	proxyGroup := group.Group("/log_sinks")
	proxyGroup.Use(middlewares...)

	handler := CreateStructProxyHandler[v1.LogSink](deps, storage.LOG_SINK_TABLE)

	proxyGroup.GET("", handler)
	proxyGroup.POST("", handler)
	proxyGroup.PATCH("", handler)

	proxyGroup.POST("/test_connection", handleLogSinkTestConnection(deps))
}
