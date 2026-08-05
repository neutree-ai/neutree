package proxies

import (
	"fmt"

	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/admission"
	"github.com/neutree-ai/neutree/pkg/storage"
)

var modelRegistryAdmissionResource = admission.NewResource[v1.ModelRegistry](storage.MODEL_REGISTRY_TABLE)

func validateModelRegistryDeleteDependencies(s storage.Storage, candidate v1.ModelRegistry) error {
	workspace, name := candidate.GetWorkspace(), candidate.GetName()
	count, err := s.Count(storage.ENDPOINT_TABLE, []storage.Filter{
		{Column: "metadata->>workspace", Operator: "eq", Value: workspace},
		{Column: "spec->model->>registry", Operator: "eq", Value: name},
	})
	if err != nil {
		return fmt.Errorf("failed to count endpoints: %w", err)
	}
	if count > 0 {
		return newLegacyDeleteDependencyError(
			10128,
			fmt.Sprintf("cannot delete model_registry '%s/%s'", workspace, name),
			fmt.Sprintf("%d endpoint(s) still reference this model registry", count),
		)
	}
	return nil
}

func RegisterModelRegistryRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) error {
	proxyGroup := group.Group("/model_registries")
	proxyGroup.Use(middlewares...)
	if err := registerModelRegistryAdmission(deps); err != nil {
		return err
	}
	var patchRunner gin.HandlerFunc
	if deps != nil && deps.Admission != nil {
		patchRunner = CreatePatchAdmissionRunner(deps, storage.MODEL_REGISTRY_TABLE, modelRegistryAdmissionResource)
	}
	handler := CreateStructProxyHandler[v1.ModelRegistry](deps, storage.MODEL_REGISTRY_TABLE)

	proxyGroup.GET("", handler)
	proxyGroup.POST("", handler)
	proxyGroup.PATCH("", withAdmissionRunner(patchRunner, handler)...)
	return nil
}

func registerModelRegistryAdmission(deps *Dependencies) error {
	if deps == nil || deps.Admission == nil {
		return nil
	}
	if err := deps.Admission.RegisterResource(modelRegistryAdmissionResource); err != nil {
		return err
	}
	return deps.Admission.RegisterHook(modelRegistryAdmissionResource, admission.ValidateDelete(
		admission.HookMeta{Name: "community.model-registry.dependencies.delete", Order: 10}, 10128,
		func(_ admission.RequestContext, _, candidate v1.ModelRegistry) error {
			return validateModelRegistryDeleteDependencies(deps.Storage, candidate)
		},
	))
}
