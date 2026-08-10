package proxies

import (
	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/admission"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// RegisterRoleAssignmentRoutes registers role assignment routes
// No fields are masked for this resource
//
// Allowed methods: GET, POST, PATCH
// Disallowed methods:
//   - PUT: Not supported (use PATCH for updates)
//   - DELETE: Use deletion timestamp pattern instead
var roleAssignmentAdmissionResource = admission.NewResource[v1.RoleAssignment](storage.ROLE_ASSIGNMENT_TABLE)

func RegisterRoleAssignmentRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) error {
	proxyGroup := group.Group("/role_assignments")
	proxyGroup.Use(middlewares...)

	createRunner, patchRunner, err := admissionRouteRunners(deps, storage.ROLE_ASSIGNMENT_TABLE, roleAssignmentAdmissionResource)
	if err != nil {
		return err
	}

	handler := CreateStructProxyHandler[v1.RoleAssignment](deps, storage.ROLE_ASSIGNMENT_TABLE)

	// Only register allowed methods
	proxyGroup.GET("", handler)
	proxyGroup.POST("", withAdmissionRunner(createRunner, handler)...)
	proxyGroup.PATCH("", withAdmissionRunner(patchRunner, handler)...)

	return nil
}
