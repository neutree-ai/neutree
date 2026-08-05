package proxies

import (
	"bytes"
	"encoding/json"

	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/recipe"
	"github.com/neutree-ai/neutree/pkg/admission"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// RegisterModelCatalogRoutes registers model catalog routes
// No fields are masked for this resource
//
// Allowed methods: GET, POST, PATCH
// Disallowed methods:
//   - PUT: Not supported (use PATCH for updates)
//   - DELETE: Use deletion timestamp pattern instead
//
// Recipe catalogs are imported client-side (the UI parses YAML / fetches URLs
// in the browser and creates each document through the normal POST path), so
// there is no server-side import endpoint. The admission hooks below are the
// single server-side gate that rejects structurally invalid recipe specs
// before they reach storage.
var modelCatalogAdmissionResource = admission.NewResource[v1.ModelCatalog](storage.MODEL_CATALOG_TABLE)

var modelCatalogCreateAdmissionRunnerOptions = CreateAdmissionRunnerOptions{
	InvalidRequestError: func(body []byte, cause error) *admission.Error {
		if admissionErr := modelCatalogPatchAdmissionRunnerOptions.InvalidRequestError(body, cause); admissionErr != nil {
			return admissionErr
		}
		return modelCatalogInvalidPayloadAdmissionError(cause)
	},
	ReadBodyError: func(cause error) *admission.Error {
		return &admission.Error{
			Code:    10223,
			Message: "failed to read request body: " + cause.Error(),
			Hint:    "Retry the request",
		}
	},
	AllowEmptyBody:       true,
	PermissiveCandidates: true,
}

var modelCatalogPatchAdmissionRunnerOptions = PatchAdmissionRunnerOptions{
	InvalidRequestError: func(body []byte, _ error) *admission.Error {
		trimmed := bytes.TrimSpace(body)
		if len(trimmed) == 0 {
			return &admission.Error{Code: 10223, Message: "invalid model_catalog payload", Hint: "Check the model catalog spec fields and types"}
		}
		if trimmed[0] == '[' {
			var catalogs []v1.ModelCatalog
			if err := json.Unmarshal(trimmed, &catalogs); err != nil {
				return modelCatalogInvalidPayloadAdmissionError(err)
			}
			return nil
		}
		var catalog v1.ModelCatalog
		if err := json.Unmarshal(trimmed, &catalog); err != nil {
			return modelCatalogInvalidPayloadAdmissionError(err)
		}
		return nil
	},
	BodyError: func(cause error) *admission.Error {
		return &admission.Error{
			Code:    10223,
			Message: "failed to read request body: " + cause.Error(),
			Hint:    "Retry the request",
		}
	},
	PermissiveCandidates: true,
}

func RegisterModelCatalogRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) error {
	proxyGroup := group.Group("/model_catalogs")
	proxyGroup.Use(middlewares...)

	if err := registerModelCatalogAdmission(deps); err != nil {
		return err
	}
	var createRunner, patchRunner gin.HandlerFunc
	if deps != nil && deps.Admission != nil {
		createRunner = CreateAdmissionRunnerWithOptions(deps.Admission, modelCatalogAdmissionResource, modelCatalogCreateAdmissionRunnerOptions)
		patchRunner = CreatePatchAdmissionRunnerWithOptions(deps, storage.MODEL_CATALOG_TABLE, modelCatalogAdmissionResource, modelCatalogPatchAdmissionRunnerOptions)
	}
	handler := CreateStructProxyHandler[v1.ModelCatalog](deps, "model_catalogs")

	// Only register allowed methods
	proxyGroup.GET("", handler)
	proxyGroup.POST("", withAdmissionRunner(createRunner, handler)...)
	proxyGroup.PATCH("", withAdmissionRunner(patchRunner, handler)...)
	return nil
}

func registerModelCatalogAdmission(deps *Dependencies) error {
	if deps == nil || deps.Admission == nil {
		return nil
	}
	if err := deps.Admission.RegisterResource(modelCatalogAdmissionResource); err != nil {
		return err
	}
	if err := deps.Admission.RegisterHook(modelCatalogAdmissionResource, admission.ValidateCreate(
		admission.HookMeta{Name: "community.model-catalog.recipe.create", Order: 10}, 10223,
		func(_ admission.RequestContext, candidate v1.ModelCatalog) error {
			return validateModelCatalogRecipeCandidate(candidate)
		},
	)); err != nil {
		return err
	}
	return deps.Admission.RegisterHook(modelCatalogAdmissionResource, admission.ValidateUpdate(
		admission.HookMeta{Name: "community.model-catalog.recipe.update", Order: 10}, 10224,
		func(_ admission.RequestContext, _, candidate v1.ModelCatalog) error {
			return validateModelCatalogRecipeCandidate(candidate)
		},
	))
}

func validateModelCatalogRecipeCandidate(candidate v1.ModelCatalog) error {
	if candidate.Spec == nil {
		return nil
	}
	if err := recipe.ValidateModelCatalogSpec(candidate.Spec); err != nil {
		return &admission.Error{Code: 10224, Message: err.Error(), Hint: "Fix the recipe definition and retry"}
	}
	return nil
}

func modelCatalogInvalidPayloadAdmissionError(cause error) *admission.Error {
	message := "invalid model_catalog payload"
	if cause != nil {
		message += ": " + cause.Error()
	}
	return &admission.Error{Code: 10223, Message: message, Hint: "Check the model catalog spec fields and types"}
}
