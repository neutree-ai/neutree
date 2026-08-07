package proxies

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/middleware"
	"github.com/neutree-ai/neutree/internal/utils/request"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// Error codes for edits refused on a built-in registry. Distinct from the
// "something still references this" code so a client can tell "you may not do
// this at all" from "not while that exists".
const (
	builtinRegistryImmutableCode = "10132"
	// credentialsPermission is what "administrators only" resolves to in this
	// schema. There is no admin flag to test: the preset admin role holds every
	// permission action, and this is the action that already means "trusted with
	// this registry's credentials" — it is the one model_registry action the
	// preset workspace-user role is not granted. Gating on it rather than
	// inventing a new action keeps the rule in the permission system instead of
	// hard-coding a role name here.
	credentialsPermission = "model_registry:read-credentials"
)

// builtinModelRegistryGuard refuses the edits that do not make sense on a
// registry this deployment provisions and keeps provisioned.
//
// The control plane re-creates a built-in registry on the next reconcile, so
// deleting one does not remove it — it makes it flicker. Changing its type turns
// it into a different kind of registry under a name that says otherwise, and the
// next reconcile changes it back. Both are better refused with an explanation
// than silently undone.
//
// Its address and credentials are a different matter: those are legitimately
// operator business — an air-gapped site pointing the registry at its own mirror,
// or attaching a token — so they are editable, but only by someone trusted with
// this registry's credentials.
//
// Everything else about a built-in registry stays editable by anyone who may
// edit registries.
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
			// The same fallthrough the deletion validation uses: a body this cannot
			// identify is one PostgREST is about to reject or apply on its own terms.
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
// registry, answering the request itself when it refuses one. It reports whether
// the request may continue.
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

	urlChanged := stringFieldChanged(spec, "url", storedURL(stored))
	credentialsChanged := stringFieldChanged(spec, "credentials", storedCredentials(stored))

	if !urlChanged && !credentialsChanged {
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
			"message": fmt.Sprintf("the address and credentials of built-in model registry '%s/%s' "+
				"can only be changed by an administrator", workspace, name),
		})
		c.Abort()

		return false
	}

	return true
}

// stringFieldChanged reports whether a patch body sets this field to something
// other than what is stored. A field the body does not mention is not a change:
// PostgREST leaves unmentioned columns alone, so refusing on its absence would
// block edits to unrelated fields.
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
// there is none. A registry that is not there is not this middleware's problem.
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
