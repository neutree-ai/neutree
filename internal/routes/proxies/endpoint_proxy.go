package proxies

import (
	"reflect"

	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/admission"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// RegisterEndpointRoutes registers endpoint routes
// No fields are masked for this resource
//
// Allowed methods: GET, POST, PATCH
// Disallowed methods:
//   - PUT: Not supported (use PATCH for updates)
//   - DELETE: Use deletion timestamp pattern instead
var endpointAdmissionResource = admission.NewResource[v1.Endpoint](storage.ENDPOINT_TABLE)

func RegisterEndpointRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) error {
	proxyGroup := group.Group("/endpoints")
	proxyGroup.Use(middlewares...)

	if err := registerEndpointAdmission(deps); err != nil {
		return err
	}
	var createRunner, patchRunner gin.HandlerFunc
	if deps != nil && deps.Admission != nil {
		createRunner = CreateAdmissionRunnerWithOptions(deps.Admission, endpointAdmissionResource, endpointCreateAdmissionRunnerOptions)
		patchRunner = CreatePatchAdmissionRunnerWithOptions(deps, storage.ENDPOINT_TABLE, endpointAdmissionResource, endpointPatchAdmissionRunnerOptions)
	}
	handler := CreateStructProxyHandler[v1.Endpoint](deps, storage.ENDPOINT_TABLE)

	// Only register allowed methods
	proxyGroup.GET("", handler)
	proxyGroup.POST("", withAdmissionRunner(createRunner, handler)...)
	proxyGroup.PATCH("", withAdmissionRunner(patchRunner, handler)...)
	return nil
}

func registerEndpointAdmission(deps *Dependencies) error {
	if deps == nil || deps.Admission == nil {
		return nil
	}
	if err := deps.Admission.RegisterResource(endpointAdmissionResource); err != nil {
		return err
	}
	if err := deps.Admission.RegisterHook(endpointAdmissionResource, admission.ValidateCreate(
		admission.HookMeta{Name: "community.endpoint.vgpu.create", Order: 10}, 10216,
		func(_ admission.RequestContext, candidate v1.Endpoint) error {
			if validationErr := validateEndpointVGPUCandidate(deps.Storage, &candidate); validationErr != nil {
				return toAdmissionError(validationErr)
			}
			return nil
		},
	)); err != nil {
		return err
	}
	return deps.Admission.RegisterHook(endpointAdmissionResource, admission.ValidateUpdate(
		admission.HookMeta{Name: "community.endpoint.vgpu.update", Order: 10}, 10218,
		func(_ admission.RequestContext, old, candidate v1.Endpoint) error {
			if !endpointVGPUFieldsChanged(&old, &candidate) {
				return nil
			}
			if validationErr := validateEndpointVGPUCandidate(deps.Storage, &candidate); validationErr != nil {
				return toAdmissionError(validationErr)
			}
			return nil
		},
	))
}

func endpointVGPUFieldsChanged(old, candidate *v1.Endpoint) bool {
	if old == nil || candidate == nil {
		return old != candidate
	}
	if old.Spec == nil || candidate.Spec == nil {
		return old.Spec != candidate.Spec
	}
	return old.Spec.Cluster != candidate.Spec.Cluster ||
		!reflect.DeepEqual(old.Spec.Replicas, candidate.Spec.Replicas) ||
		!reflect.DeepEqual(old.Spec.Resources, candidate.Spec.Resources)
}
