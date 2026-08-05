package proxies

import (
	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/admission"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// RegisterEngineRoutes registers engine routes
// No fields are masked for this resource
//
// Allowed methods: GET, POST, PATCH
// Disallowed methods:
//   - PUT: Not supported (use PATCH for updates)
//   - DELETE: Use deletion timestamp pattern instead
var engineAdmissionResource = admission.NewResource[v1.Engine](storage.ENGINE_TABLE)

func RegisterEngineRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) error {
	proxyGroup := group.Group("/engines")
	proxyGroup.Use(middlewares...)

	createRunner, patchRunner, err := admissionRouteRunners(deps, storage.ENGINE_TABLE, engineAdmissionResource)
	if err != nil {
		return err
	}
	handler := CreateStructProxyHandler[v1.Engine](deps, storage.ENGINE_TABLE)

	// Only register allowed methods
	proxyGroup.GET("", handler)
	proxyGroup.POST("", withAdmissionRunner(createRunner, handler)...)
	proxyGroup.PATCH("", withAdmissionRunner(patchRunner, handler)...)
	return nil
}
