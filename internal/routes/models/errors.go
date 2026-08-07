package models

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"k8s.io/klog/v2"

	"github.com/neutree-ai/neutree/internal/model_registry"
)

// Why a registry could not answer. A client showing this to someone needs to
// tell apart the cases that call for different actions — fix the registry's
// credentials, wait, look somewhere else — and prose in a message cannot be
// branched on.
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

// respondRegistryError answers a request that failed inside a registry, and
// reports whether it did. Errors it does not recognise are left to the caller,
// which knows what its own generic failure should look like.
//
// The status codes are chosen from what the user can do:
//
//   - 400 for "this registry, as configured, cannot serve this" — the kind that
//     cannot do it, and the one whose credentials it rejects. Both are fixed by
//     changing the registry, not by changing the request or by trying again.
//   - 404 for a model or a file that is not there.
//   - 429 passed straight through, because that is exactly what happened and a
//     client already knows what it means.
func respondRegistryError(c *gin.Context, context string, err error) bool {
	switch {
	case errors.Is(err, model_registry.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{
			"message": fmt.Sprintf("%s: not found", context),
			"reason":  reasonNotFound,
		})
	case errors.Is(err, model_registry.ErrNotSupported):
		c.JSON(http.StatusBadRequest, gin.H{
			"message": fmt.Sprintf("%s: this model registry cannot do that: %v", context, err),
			"reason":  reasonNotSupported,
		})
	case errors.Is(err, model_registry.ErrUnauthorized):
		c.JSON(http.StatusBadRequest, gin.H{
			"message": fmt.Sprintf("%s: %v", context, err),
			"reason":  reasonUnauthorized,
		})
	case errors.Is(err, model_registry.ErrRateLimited):
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
