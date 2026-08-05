package proxies

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/admission"
	"github.com/neutree-ai/neutree/pkg/storage"
)

var imageRegistryAdmissionResource = admission.NewResource[v1.ImageRegistry](storage.IMAGE_REGISTRY_TABLE)

const (
	imageRegistryURLCreateAdmissionErrorCode = 10225
	imageRegistryURLUpdateAdmissionErrorCode = 10226
)

var imageRegistryCreateAdmissionRunnerOptions = CreateAdmissionRunnerOptions{
	InvalidRequestResponse: legacyImageRegistryURLParseError,
	ReadBodyResponse: func(cause error) error {
		return newLegacyImageRegistryURLAdmissionError(fmt.Sprintf("failed to read request body: %v", cause))
	},
	RejectArray:          true,
	PermissiveCandidates: true,
}

var imageRegistryPatchAdmissionRunnerOptions = PatchAdmissionRunnerOptions{
	InvalidRequestResponse: legacyImageRegistryURLParseError,
	BodyResponse: func(cause error) error {
		return newLegacyImageRegistryURLAdmissionError(fmt.Sprintf("failed to read request body: %v", cause))
	},
	PermissiveCandidates:  true,
	DropEmptyMaskedFields: []string{"spec.authconfig"},
}

type legacyImageRegistryURLAdmissionError struct {
	message string
}

func newLegacyImageRegistryURLAdmissionError(message string) error {
	return &legacyImageRegistryURLAdmissionError{message: message}
}

func (e *legacyImageRegistryURLAdmissionError) Error() string {
	return e.message
}

func (e *legacyImageRegistryURLAdmissionError) legacyAdmissionResponse() (int, any) {
	return http.StatusBadRequest, gin.H{"error": e.message}
}

func validateImageRegistryDeleteDependencies(s storage.Storage, candidate v1.ImageRegistry) error {
	workspace, name := candidate.GetWorkspace(), candidate.GetName()

	count, err := s.Count(storage.CLUSTERS_TABLE, []storage.Filter{
		{Column: "metadata->>workspace", Operator: "eq", Value: workspace},
		{Column: "spec->>image_registry", Operator: "eq", Value: name},
	})
	if err != nil {
		return fmt.Errorf("failed to count clusters: %w", err)
	}

	if count > 0 {
		return newLegacyDeleteDependencyError(
			10127,
			fmt.Sprintf("cannot delete image_registry '%s/%s'", workspace, name),
			fmt.Sprintf("%d cluster(s) still reference this image registry", count),
		)
	}

	return nil
}

func RegisterImageRegistryRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) error {
	proxyGroup := group.Group("/image_registries")
	proxyGroup.Use(middlewares...)

	if err := registerImageRegistryAdmission(deps); err != nil {
		return err
	}

	var createRunner, patchRunner gin.HandlerFunc
	if deps != nil && deps.Admission != nil {
		createRunner = CreateAdmissionRunnerWithOptions(deps.Admission, imageRegistryAdmissionResource, imageRegistryCreateAdmissionRunnerOptions)
		patchRunner = CreatePatchAdmissionRunnerWithOptions(deps, storage.IMAGE_REGISTRY_TABLE, imageRegistryAdmissionResource, imageRegistryPatchAdmissionRunnerOptions)
	}

	handler := CreateStructProxyHandler[v1.ImageRegistry](deps, storage.IMAGE_REGISTRY_TABLE)

	proxyGroup.GET("", handler)
	proxyGroup.POST("", withAdmissionRunner(createRunner, handler)...)
	proxyGroup.PATCH("", withAdmissionRunner(patchRunner, handler)...)

	return nil
}

func registerImageRegistryAdmission(deps *Dependencies) error {
	if deps == nil || deps.Admission == nil {
		return nil
	}

	if err := deps.Admission.RegisterResource(imageRegistryAdmissionResource); err != nil {
		return err
	}

	if err := deps.Admission.RegisterHook(imageRegistryAdmissionResource, admission.ValidateCreate(
		admission.HookMeta{Name: "community.image-registry.url.create", Order: 10}, imageRegistryURLCreateAdmissionErrorCode,
		func(_ admission.RequestContext, candidate v1.ImageRegistry) error {
			return validateImageRegistryURLCandidate(candidate)
		},
	)); err != nil {
		return err
	}

	if err := deps.Admission.RegisterHook(imageRegistryAdmissionResource, admission.ValidateUpdate(
		admission.HookMeta{Name: "community.image-registry.url.update", Order: 10}, imageRegistryURLUpdateAdmissionErrorCode,
		func(_ admission.RequestContext, _, candidate v1.ImageRegistry) error {
			return validateImageRegistryURLCandidate(candidate)
		},
	)); err != nil {
		return err
	}

	return deps.Admission.RegisterHook(imageRegistryAdmissionResource, admission.ValidateDelete(
		admission.HookMeta{Name: "community.image-registry.dependencies.delete", Order: 10}, 10127,
		func(_ admission.RequestContext, _, candidate v1.ImageRegistry) error {
			return validateImageRegistryDeleteDependencies(deps.Storage, candidate)
		},
	))
}

func validateImageRegistryURLCandidate(candidate v1.ImageRegistry) error {
	if candidate.Spec == nil || candidate.Spec.URL == "" {
		return nil
	}

	if _, err := util.GetImageRegistryHost(&candidate); err != nil {
		return newLegacyImageRegistryURLAdmissionError(fmt.Sprintf("invalid image registry url: %v", err))
	}

	return nil
}

func legacyImageRegistryURLParseError(body []byte, cause error) error {
	var imageRegistry v1.ImageRegistry
	if err := json.Unmarshal(body, &imageRegistry); err != nil {
		return newLegacyImageRegistryURLAdmissionError(fmt.Sprintf("failed to parse image registry: %v", err))
	}

	return newLegacyImageRegistryURLAdmissionError(fmt.Sprintf("failed to parse image registry: %v", cause))
}
