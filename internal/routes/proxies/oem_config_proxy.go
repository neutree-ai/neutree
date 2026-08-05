package proxies

import (
	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/admission"
)

const oemConfigTable = "oem_configs"

var oemConfigAdmissionResource = admission.NewResource[v1.OEMConfig](oemConfigTable)

// RegisterOEMConfigRoutes registers OEM config routes
// No fields are masked for this resource
//
// Allowed methods: GET, POST, PATCH
// Disallowed methods:
//   - PUT: Not supported (use PATCH for updates)
//   - DELETE: Use deletion timestamp pattern instead
func RegisterOEMConfigRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) error {
	proxyGroup := group.Group("/oem_configs")

	createRunner, patchRunner, err := admissionRouteRunners(deps, oemConfigTable, oemConfigAdmissionResource)
	if err != nil {
		return err
	}

	handler := CreateStructProxyHandler[v1.OEMConfig](deps, oemConfigTable)

	// Only register allowed methods
	proxyGroup.GET("", handler)
	proxyGroup.POST("", withRouteMiddlewares(middlewares, withAdmissionRunner(createRunner, handler))...)
	proxyGroup.PATCH("", withRouteMiddlewares(middlewares, withAdmissionRunner(patchRunner, handler))...)

	return nil
}
