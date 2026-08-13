package models

import (
	stderrors "errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/model_registry"
)

// Why a registry could not answer. Clients branch on these, so the distinctions
// matter: fix the registry's credentials, wait, or look elsewhere.
const (
	// reasonNotFound — the registry answered and the thing is not there.
	reasonNotFound = "not_found"
	// reasonNotSupported — this kind of registry cannot do this at all.
	reasonNotSupported = "not_supported"
	// reasonUnauthorized — the registry refused our credentials. Someone has to
	// give it a token, or a better one; retrying will not help.
	reasonUnauthorized = "registry_unauthorized"
	// reasonRateLimited — the registry is throttling us. Nothing is wrong; the
	// answer is to come back later.
	reasonRateLimited = "rate_limited"
	// reasonUnavailable — the registry could not be reached or failed in a way we
	// cannot classify. Worth retrying.
	reasonUnavailable = "unavailable"
)

// refuseReadOnlyRegistry answers a write aimed at a registry that has no write
// operations. It goes through respondRegistryError so that a refusal looks the
// same whichever verb produced it.
func refuseReadOnlyRegistry(c *gin.Context, context string) {
	respondRegistryError(c, context,
		errors.Wrap(model_registry.ErrNotSupported,
			"this model registry is read-only"))
}

// registryAcceptsWrites reports whether a write is worth attempting. Public
// registries are read-only, and knowing that from the stored row rather than
// from the failed operation is what lets uploadModel refuse before reading a
// request body. The per-operation ErrNotSupported remains the backstop.
func registryAcceptsWrites(registry *v1.ModelRegistry) bool {
	if registry == nil || registry.Spec == nil {
		return true
	}

	return v1.VisibilityForModelRegistryType(registry.Spec.Type) != v1.ModelRegistryVisibilityPublic
}

// respondRegistryError answers a request that failed inside a registry and
// reports whether it did; unrecognised errors are left to the caller.
//
// Status codes follow what the user can do about it: 400 when the registry as
// configured cannot serve the request (wrong kind, or rejected credentials),
// 404 when the thing is not there, and 429 passed through as itself.
func respondRegistryError(c *gin.Context, context string, err error) bool {
	switch {
	case stderrors.Is(err, model_registry.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{
			"message": fmt.Sprintf("%s: not found", context),
			"reason":  reasonNotFound,
		})
	case stderrors.Is(err, model_registry.ErrNotSupported):
		c.JSON(http.StatusBadRequest, gin.H{
			"message": fmt.Sprintf("%s: this model registry cannot do that: %v", context, err),
			"reason":  reasonNotSupported,
		})
	case stderrors.Is(err, model_registry.ErrUnauthorized):
		c.JSON(http.StatusBadRequest, gin.H{
			"message": fmt.Sprintf("%s: %v", context, err),
			"reason":  reasonUnauthorized,
		})
	case stderrors.Is(err, model_registry.ErrRateLimited):
		klog.Warningf("%s: %v", context, err)
		c.JSON(http.StatusTooManyRequests, gin.H{
			"message": fmt.Sprintf("%s: %v", context, err),
			"reason":  reasonRateLimited,
		})
	default:
		return false
	}

	return true
}
