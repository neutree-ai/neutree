package proxies

import (
	"fmt"
	"strconv"

	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/middleware"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// userModelRegistryCount counts the model registries in a workspace that would
// keep a user from deleting it: the ones they created.
//
// Registries the control plane provisions are excluded. They exist in every
// workspace whenever the built-in public registries option is on, and the API
// refuses to delete them, so counting them would make every workspace
// permanently undeletable. The built-in engines are excluded from this check the
// same way — by not being counted at all — and this keeps the two consistent.
//
// Registries already marked for deletion are excluded for the same reason the
// model deletion path excludes soft-deleted endpoints: an object on its way out
// must not block the removal that releases it.
//
// Counted in Go rather than through a filter because the marker is a JSON key
// containing a dot and a slash, which a PostgREST column expression cannot
// address without quoting rules this codebase does not otherwise rely on.
func userModelRegistryCount(s storage.Storage, workspace string) (int, error) {
	registries, err := s.ListModelRegistry(storage.ListOption{
		Filters: []storage.Filter{
			{Column: "metadata->workspace", Operator: "eq", Value: strconv.Quote(workspace)},
		},
	})
	if err != nil {
		return 0, fmt.Errorf("failed to count %s: %w", storage.MODEL_REGISTRY_TABLE, err)
	}

	count := 0

	for i := range registries {
		metadata := registries[i].Metadata
		if metadata == nil {
			count++

			continue
		}

		if v1.IsBuiltin(metadata.Annotations) || metadata.DeletionTimestamp != "" {
			continue
		}

		count++
	}

	return count, nil
}

func validateWorkspaceDeletion(s storage.Storage) middleware.DeletionValidatorFunc {
	return func(workspace, name string) error {
		counts := make(map[string]int)
		tables := []string{
			storage.ENDPOINT_TABLE,
			storage.CLUSTERS_TABLE,
			storage.MODEL_REGISTRY_TABLE,
			storage.IMAGE_REGISTRY_TABLE,
			storage.MODEL_CATALOG_TABLE,
			storage.ROLE_TABLE,
			storage.API_KEY_TABLE,
		}

		for _, table := range tables {
			count, err := s.Count(table, []storage.Filter{
				{Column: "metadata->>workspace", Operator: "eq", Value: name},
			})
			if err != nil {
				return fmt.Errorf("failed to count %s: %w", table, err)
			}

			counts[table] = count
		}

		registries, err := userModelRegistryCount(s, name)
		if err != nil {
			return err
		}

		counts[storage.MODEL_REGISTRY_TABLE] = registries

		count, err := s.Count(storage.ROLE_ASSIGNMENT_TABLE, []storage.Filter{
			{Column: "spec->>workspace", Operator: "eq", Value: name},
		})
		if err != nil {
			return fmt.Errorf("failed to count role assignments: %w", err)
		}

		counts[storage.ROLE_ASSIGNMENT_TABLE] = count

		totalCount := 0
		for _, count := range counts {
			totalCount += count
		}

		if totalCount > 0 {
			hint := "Resources still exist in this workspace:"

			for resourceType, count := range counts {
				if count > 0 {
					hint += fmt.Sprintf("\n- %s: %d", resourceType, count)
				}
			}

			return &middleware.DeletionError{
				Code:    "10125",
				Message: fmt.Sprintf("cannot delete workspace '%s'", name),
				Hint:    hint,
			}
		}

		return nil
	}
}

func RegisterWorkspaceRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) {
	proxyGroup := group.Group("/workspaces")
	proxyGroup.Use(middlewares...)

	deletionValidation := middleware.DeletionValidation(
		storage.WORKSPACE_TABLE,
		validateWorkspaceDeletion(deps.Storage),
	)
	handler := CreateStructProxyHandler[v1.Workspace](deps, storage.WORKSPACE_TABLE)

	proxyGroup.GET("", handler)
	proxyGroup.POST("", handler)
	proxyGroup.PATCH("", deletionValidation, handler)
}
