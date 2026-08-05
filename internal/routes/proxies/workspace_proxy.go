package proxies

import (
	"fmt"

	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/admission"
	"github.com/neutree-ai/neutree/pkg/storage"
)

var workspaceAdmissionResource = admission.NewResource[v1.Workspace](storage.WORKSPACE_TABLE)

type workspaceDependencyCount struct {
	resourceType string
	count        int
}

func validateWorkspaceDeleteDependencies(s storage.Storage, candidate v1.Workspace) error {
	name := candidate.GetName()
	counts := make([]workspaceDependencyCount, 0, 8)
	for _, table := range []string{
		storage.ENDPOINT_TABLE,
		storage.CLUSTERS_TABLE,
		storage.MODEL_REGISTRY_TABLE,
		storage.IMAGE_REGISTRY_TABLE,
		storage.MODEL_CATALOG_TABLE,
		storage.ROLE_TABLE,
		storage.API_KEY_TABLE,
	} {
		count, err := s.Count(table, []storage.Filter{{Column: "metadata->>workspace", Operator: "eq", Value: name}})
		if err != nil {
			return fmt.Errorf("failed to count %s: %w", table, err)
		}
		counts = append(counts, workspaceDependencyCount{resourceType: table, count: count})
	}

	count, err := s.Count(storage.ROLE_ASSIGNMENT_TABLE, []storage.Filter{{Column: "spec->>workspace", Operator: "eq", Value: name}})
	if err != nil {
		return fmt.Errorf("failed to count role assignments: %w", err)
	}
	counts = append(counts, workspaceDependencyCount{resourceType: storage.ROLE_ASSIGNMENT_TABLE, count: count})

	totalCount := 0
	hint := "Resources still exist in this workspace:"
	for _, dependency := range counts {
		totalCount += dependency.count
		if dependency.count > 0 {
			hint += fmt.Sprintf("\n- %s: %d", dependency.resourceType, dependency.count)
		}
	}
	if totalCount > 0 {
		return newLegacyDeleteDependencyError(10125, fmt.Sprintf("cannot delete workspace '%s'", name), hint)
	}
	return nil
}

func RegisterWorkspaceRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) error {
	proxyGroup := group.Group("/workspaces")
	proxyGroup.Use(middlewares...)
	if err := registerWorkspaceAdmission(deps); err != nil {
		return err
	}
	var patchRunner gin.HandlerFunc
	if deps != nil && deps.Admission != nil {
		patchRunner = CreatePatchAdmissionRunner(deps, storage.WORKSPACE_TABLE, workspaceAdmissionResource)
	}
	handler := CreateStructProxyHandler[v1.Workspace](deps, storage.WORKSPACE_TABLE)

	proxyGroup.GET("", handler)
	proxyGroup.POST("", handler)
	proxyGroup.PATCH("", withAdmissionRunner(patchRunner, handler)...)
	return nil
}

func registerWorkspaceAdmission(deps *Dependencies) error {
	if deps == nil || deps.Admission == nil {
		return nil
	}
	if err := deps.Admission.RegisterResource(workspaceAdmissionResource); err != nil {
		return err
	}
	return deps.Admission.RegisterHook(workspaceAdmissionResource, admission.ValidateDelete(
		admission.HookMeta{Name: "community.workspace.dependencies.delete", Order: 10}, 10125,
		func(_ admission.RequestContext, _, candidate v1.Workspace) error {
			return validateWorkspaceDeleteDependencies(deps.Storage, candidate)
		},
	))
}
