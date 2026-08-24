package proxies

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/middleware"
	"github.com/neutree-ai/neutree/internal/model_registry"
	"github.com/neutree-ai/neutree/internal/utils/request"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// builtinRegistryImmutableCode marks an edit refused outright, as opposed to one
// blocked by an existing reference.
const (
	builtinRegistryImmutableCode = "10132"
	// credentialsPermission is what "administrators only" resolves to here. There
	// is no admin flag to test: the preset admin role holds every permission action,
	// and this is the one model_registry action the preset workspace-user role does
	// not get. Gating on it keeps the rule in the permission system rather than
	// hard-coding a role name.
	credentialsPermission = "model_registry:read-credentials"
)

// builtinModelRegistryGuard refuses edits that a built-in registry cannot
// meaningfully take, and stops the marker that identifies one from being set or
// removed by hand.
//
// Deleting a built-in registry, changing its type, or changing its address are
// all undone by the next reconcile, so they are refused with an explanation
// rather than silently reverted. The address in particular is owned by the
// deployment configuration: an operator who edits values.yaml has to see it take
// effect, which it cannot if the API also lets it be edited out from under them.
//
// Credentials are the exception and belong to the user, so they stay editable —
// by someone trusted with them. Everything else about the registry is
// unaffected.
func builtinModelRegistryGuard(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method != http.MethodPatch {
			c.Next()

			return
		}

		bodyCtx, err := request.ExtractBody(c)
		if err != nil {
			c.Next()

			return
		}

		request.RestoreBody(c, bodyCtx.BodyBytes)

		workspace, name, err := request.ExtractResourceIdentifiers(bodyCtx.BodyMap)
		if err != nil {
			// Same fallthrough as the deletion validation: a body this cannot identify is
			// one PostgREST will reject or apply on its own terms.
			klog.V(4).Infof("Could not extract model registry identifiers: %v, skipping built-in checks", err)
			c.Next()

			return
		}

		stored, err := findStoredModelRegistry(deps.Storage, workspace, name)
		if err != nil {
			klog.Errorf("Failed to look up model registry %s/%s: %v", workspace, name, err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"message": fmt.Sprintf("failed to look up model registry: %v", err),
			})
			c.Abort()

			return
		}

		if builtinAnnotationChanged(bodyCtx.BodyMap, stored) {
			abortBuiltinRegistry(c, workspace, name,
				"the "+v1.BuiltinAnnotationKey+" annotation identifies a registry the control plane owns "+
					"and cannot be set or removed by hand")

			return
		}

		if stored == nil || stored.Metadata == nil || !v1.IsBuiltin(stored.Metadata.Annotations) {
			c.Next()

			return
		}

		if request.IsSoftDeleteRequest(bodyCtx.BodyMap) {
			abortBuiltinRegistry(c, workspace, name,
				"a built-in model registry cannot be deleted; turn off the built-in public model registries "+
					"option to stop it being provisioned")

			return
		}

		if !guardBuiltinRegistrySpec(c, deps, bodyCtx.BodyMap, stored, workspace, name) {
			return
		}

		c.Next()
	}
}

// guardBuiltinRegistrySpec checks the spec changes in a patch of a built-in
// registry, answering the request itself when it refuses one, and reports
// whether the request may continue.
func guardBuiltinRegistrySpec(c *gin.Context, deps *Dependencies, bodyMap map[string]interface{},
	stored *v1.ModelRegistry, workspace, name string) bool {
	spec, ok := bodyMap["spec"].(map[string]interface{})
	if !ok {
		return true
	}

	if stringFieldChanged(spec, "type", string(stored.Spec.Type)) {
		abortBuiltinRegistry(c, workspace, name,
			"a built-in model registry's type cannot be changed")

		return false
	}

	if stringFieldChanged(spec, "url", storedURL(stored)) {
		abortBuiltinRegistry(c, workspace, name, builtinRegistryURLRefusal(stored))

		return false
	}

	if !stringFieldChanged(spec, "credentials", storedCredentials(stored)) {
		return true
	}

	allowed, err := middleware.CheckWorkspacePermission(deps.Storage, c.GetString("user_id"),
		workspace, credentialsPermission)
	if err != nil {
		klog.Errorf("Failed to check permission %s: %v", credentialsPermission, err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"message": fmt.Sprintf("failed to check permissions: %v", err),
		})
		c.Abort()

		return false
	}

	if !allowed {
		c.JSON(http.StatusForbidden, gin.H{
			"error":    "insufficient permissions",
			"required": credentialsPermission,
			"message": fmt.Sprintf("the credentials of built-in model registry '%s/%s' "+
				"can only be changed by an administrator", workspace, name),
		})
		c.Abort()

		return false
	}

	return true
}

// stringFieldChanged reports whether a patch body sets this field to something
// other than what is stored. A field the body does not mention is not a change:
// PostgREST leaves unmentioned columns alone.
func stringFieldChanged(spec map[string]interface{}, field, current string) bool {
	raw, present := spec[field]
	if !present {
		return false
	}

	value, ok := raw.(string)
	if !ok {
		return false
	}

	return value != current
}

func storedURL(stored *v1.ModelRegistry) string {
	if stored.Spec == nil {
		return ""
	}

	return stored.Spec.Url
}

func storedCredentials(stored *v1.ModelRegistry) string {
	if stored.Spec == nil {
		return ""
	}

	return stored.Spec.Credentials
}

func abortBuiltinRegistry(c *gin.Context, workspace, name, hint string) {
	c.Header("X-Powered-By", "Neutree")
	c.JSON(http.StatusBadRequest, gin.H{
		"code":    builtinRegistryImmutableCode,
		"message": fmt.Sprintf("cannot modify built-in model registry '%s/%s'", workspace, name),
		"hint":    hint,
	})
	c.Abort()
}

// findStoredModelRegistry returns the registry a patch is aimed at, or nil when
// there is none.
func findStoredModelRegistry(s storage.Storage, workspace, name string) (*v1.ModelRegistry, error) {
	registries, err := s.ListModelRegistry(storage.ListOption{
		Filters: []storage.Filter{
			{Column: "metadata->workspace", Operator: "eq", Value: strconv.Quote(workspace)},
			{Column: "metadata->name", Operator: "eq", Value: strconv.Quote(name)},
		},
	})
	if err != nil {
		return nil, err
	}

	if len(registries) == 0 {
		return nil, nil
	}

	return &registries[0], nil
}

// builtinRegistryURLRefusal explains where a built-in registry's address is
// really set. Each hub has its own setting, so the flag is looked up from the
// registry's kind rather than written into the sentence: a ModelScope registry
// that told the operator to edit --hugging-face-endpoint would send them to
// change a value that has no effect on it.
func builtinRegistryURLRefusal(stored *v1.ModelRegistry) string {
	message := "the address of a built-in model registry comes from this deployment's configuration"

	if flag := model_registry.EndpointFlagForModelRegistryType(stored.Spec.Type); flag != "" {
		message += " (" + flag + ")"
	}

	return message + "; change it there, since the reconcile restores it otherwise"
}

// builtinAnnotationChanged reports whether a patch adds or removes the built-in
// marker.
//
// The marker is an identity, and identity must not be self-service in either
// direction. Adding it to a user's own registry would hand it to the control
// plane, which would then reconcile its address to the configured one; removing
// it from a built-in registry would leave a row that provisioning no longer
// recognises and this guard no longer protects.
//
// A patch that leaves the marker as it already is passes: PostgREST replaces the
// whole metadata composite, so a client resending the object it just read must
// not be refused for including it.
func builtinAnnotationChanged(bodyMap map[string]interface{}, stored *v1.ModelRegistry) bool {
	metadata, ok := bodyMap["metadata"].(map[string]interface{})
	if !ok {
		return false
	}

	annotations, present := metadata["annotations"]
	if !present {
		return false
	}

	wanted := false

	if set, ok := annotations.(map[string]interface{}); ok {
		value, _ := set[v1.BuiltinAnnotationKey].(string)
		wanted = value == v1.BuiltinAnnotationValue
	}

	current := false
	if stored != nil && stored.Metadata != nil {
		current = v1.IsBuiltin(stored.Metadata.Annotations)
	}

	return wanted != current
}
