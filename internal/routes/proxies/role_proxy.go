package proxies

import (
	"fmt"

	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/admission"
	"github.com/neutree-ai/neutree/pkg/storage"
)

var roleAdmissionResource = admission.NewResource[v1.Role](storage.ROLE_TABLE)

func validateRoleDeleteDependencies(s storage.Storage, candidate v1.Role) error {
	workspace, name := candidate.GetWorkspace(), candidate.GetName()
	filters := []storage.Filter{{Column: "spec->>role", Operator: "eq", Value: name}}

	if workspace == "" {
		filters = append(filters, storage.Filter{Column: "metadata->>workspace", Operator: "is", Value: "null"})
	} else {
		filters = append(filters, storage.Filter{Column: "metadata->>workspace", Operator: "eq", Value: workspace})
	}

	count, err := s.Count(storage.ROLE_ASSIGNMENT_TABLE, filters)
	if err != nil {
		return fmt.Errorf("failed to count role assignments: %w", err)
	}

	if count == 0 {
		return nil
	}

	displayWorkspace := workspace
	if displayWorkspace == "" {
		displayWorkspace = "global"
	}

	return newLegacyDeleteDependencyError(
		10129,
		fmt.Sprintf("cannot delete role '%s/%s'", displayWorkspace, name),
		fmt.Sprintf("%d role assignment(s) still reference this role", count),
	)
}

func RegisterRoleRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) error {
	proxyGroup := group.Group("/roles")
	proxyGroup.Use(middlewares...)

	if err := registerRoleAdmission(deps); err != nil {
		return err
	}

	var createRunner, patchRunner gin.HandlerFunc
	if deps != nil && deps.Admission != nil {
		createRunner = CreateAdmissionRunnerWithOptions(deps.Admission, roleAdmissionResource, legacyCreateAdmissionRunnerOptions)
		patchRunner = CreatePatchAdmissionRunner(deps, storage.ROLE_TABLE, roleAdmissionResource)
	}

	handler := CreateStructProxyHandler[v1.Role](deps, storage.ROLE_TABLE)

	proxyGroup.GET("", handler)
	proxyGroup.POST("", withAdmissionRunner(createRunner, handler)...)
	proxyGroup.PATCH("", withAdmissionRunner(patchRunner, handler)...)

	return nil
}

func registerRoleAdmission(deps *Dependencies) error {
	if deps == nil || deps.Admission == nil {
		return nil
	}

	if err := deps.Admission.RegisterResource(roleAdmissionResource); err != nil {
		return err
	}

	return deps.Admission.RegisterHook(roleAdmissionResource, admission.ValidateDelete(
		admission.HookMeta{Name: "community.role.dependencies.delete", Order: 10}, 10129,
		func(_ admission.RequestContext, _, candidate v1.Role) error {
			return validateRoleDeleteDependencies(deps.Storage, candidate)
		},
	))
}
