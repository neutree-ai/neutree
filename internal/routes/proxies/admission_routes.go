package proxies

import (
	"github.com/gin-gonic/gin"

	"github.com/neutree-ai/neutree/pkg/admission"
)

// legacyCreateAdmissionRunnerOptions keeps resources that historically
// forwarded empty, unknown-field, and malformed request bodies compatible
// while still running registered create hooks for valid object candidates.
var legacyCreateAdmissionRunnerOptions = CreateAdmissionRunnerOptions{
	AllowEmptyBody:            true,
	PermissiveCandidates:      true,
	PassthroughInvalidRequest: true,
}

func admissionRouteRunners[T any](deps *Dependencies, tableName string, resource admission.Resource[T]) (gin.HandlerFunc, gin.HandlerFunc, error) {
	if err := registerAdmissionResource(deps, resource); err != nil {
		return nil, nil, err
	}

	if deps == nil || deps.Admission == nil {
		return nil, nil, nil
	}

	return CreateAdmissionRunner(deps.Admission, resource), CreatePatchAdmissionRunner(deps, tableName, resource), nil
}

func admissionPatchRunner[T any](deps *Dependencies, tableName string, resource admission.Resource[T]) (gin.HandlerFunc, error) {
	if err := registerAdmissionResource(deps, resource); err != nil {
		return nil, err
	}

	if deps == nil || deps.Admission == nil {
		return nil, nil
	}

	return CreatePatchAdmissionRunner(deps, tableName, resource), nil
}

func registerAdmissionResource[T any](deps *Dependencies, resource admission.Resource[T]) error {
	if deps == nil || deps.Admission == nil {
		return nil
	}

	return deps.Admission.RegisterResource(resource)
}

func withAdmissionRunner(runner gin.HandlerFunc, handler gin.HandlerFunc) []gin.HandlerFunc {
	if runner == nil {
		return []gin.HandlerFunc{handler}
	}

	return []gin.HandlerFunc{runner, handler}
}

func withRouteMiddlewares(middlewares, handlers []gin.HandlerFunc) []gin.HandlerFunc {
	combined := make([]gin.HandlerFunc, 0, len(middlewares)+len(handlers))
	combined = append(combined, middlewares...)

	return append(combined, handlers...)
}
