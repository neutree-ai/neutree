package proxies

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// rejectAPIKeyForceDelete refuses any api_key write carrying the force-delete
// annotation.
//
// Force delete exists so a resource whose external state cannot be reached is not
// stuck forever, trading an orphaned resource for a control plane the user can move
// on from. For an api_key that external state is the gateway consumer gating
// data-plane access, so the trade does not hold: skipping it leaves a credential
// that still works on the gateway with nothing left in the control plane to revoke
// it (NEU-650). The controller therefore ignores the annotation, and this rejects it
// at the edge so a caller who sets it learns why it had no effect instead of
// believing the key was force-deleted.
//
// Only api_key is affected — cluster, endpoint and static_node keep force delete,
// where the leak is compute and cost rather than a live credential.
func rejectAPIKeyForceDelete() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method != http.MethodPatch && c.Request.Method != http.MethodPost {
			c.Next()
			return
		}

		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.JSON(http.StatusBadRequest, &validationError{
				Code:    apiKeyInvalidPayloadCode,
				Message: "invalid api_key payload",
				Hint:    err.Error(),
			})
			c.Abort()

			return
		}

		c.Request.Body = io.NopCloser(bytes.NewReader(body))

		if len(bytes.TrimSpace(body)) == 0 {
			c.Next()
			return
		}

		if payloadHasProjectID(body) {
			c.JSON(http.StatusBadRequest, &validationError{
				Code:    apiKeyProjectMutationRejectedCode,
				Message: "api_key project_id can only be changed through move_api_keys",
				Hint:    "Use the API key migration endpoint so workspace, project state, permissions, and name conflicts are validated atomically.",
			})
			c.Abort()
			return
		}

		if !payloadHasForceDeleteAnnotation(body) {
			c.Next()
			return
		}

		c.JSON(http.StatusBadRequest, &validationError{
			Code:    apiKeyForceDeleteRejectedCode,
			Message: "api_key does not support force delete",
			Hint: "Deleting an API key must revoke its gateway credential first; forcing the deletion would leave " +
				"the key usable on the gateway with no way to revoke it. Remove the " + v1.ForceDeleteAnnotationKey +
				" annotation and retry — if the deletion does not complete, the gateway is unreachable and needs " +
				"to be restored.",
		})
		c.Abort()
	}
}

const (
	apiKeyInvalidPayloadCode      = "10225"
	apiKeyForceDeleteRejectedCode = "10226"
	apiKeyProjectMutationRejectedCode = "10227"
)

func payloadHasProjectID(body []byte) bool {
	var payload interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}
	return payloadContainsProjectID(payload)
}

func payloadContainsProjectID(payload interface{}) bool {
	switch value := payload.(type) {
	case map[string]interface{}:
		_, found := value["project_id"]
		return found
	case []interface{}:
		for _, item := range value {
			if payloadContainsProjectID(item) {
				return true
			}
		}
	}
	return false
}

// payloadHasForceDeleteAnnotation reports whether the request body sets the
// force-delete annotation. Bodies that do not parse as an api_key are passed
// through untouched for PostgREST to reject, so that malformed input keeps its
// existing error rather than being reported as a force-delete problem.
func payloadHasForceDeleteAnnotation(body []byte) bool {
	trimmed := bytes.TrimSpace(body)

	if len(trimmed) > 0 && trimmed[0] == '[' {
		var apiKeys []v1.ApiKey
		if err := json.Unmarshal(trimmed, &apiKeys); err != nil {
			return false
		}

		for i := range apiKeys {
			if apiKeyHasForceDeleteAnnotation(&apiKeys[i]) {
				return true
			}
		}

		return false
	}

	var apiKey v1.ApiKey
	if err := json.Unmarshal(trimmed, &apiKey); err != nil {
		return false
	}

	return apiKeyHasForceDeleteAnnotation(&apiKey)
}

func apiKeyHasForceDeleteAnnotation(apiKey *v1.ApiKey) bool {
	if apiKey.Metadata == nil {
		return false
	}

	return v1.IsForceDelete(apiKey.Metadata.Annotations)
}
