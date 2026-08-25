package proxies

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

const (
	externalEndpointKind = "external endpoint"

	externalEndpointInvalidPayloadCode = "10231"
	externalEndpointInvalidNameCode    = "10232"
)

// validateExternalEndpoint enforces the metadata.name contract at the API
// boundary.
//
// The name is an identity, not a label: getExternalEndpointRoutePath pastes it
// verbatim into the Kong route path, into the service URL handed back to the
// user, and into the gateway plugin's route_prefix, and BuildNeutreeACLGroup
// pastes it into the ACL group. A name carrying a space or a non-ASCII rune is
// percent-encoded by the client, so it matches no Kong route: the resource is
// created, reads back fine, advertises a URL, and answers 404 forever. UI
// validation cannot be the only guard, because the CLI and every other caller
// reach the same table (NEU-714).
//
// A human-readable name belongs in metadata.display_name, which this leaves
// alone.
func validateExternalEndpoint() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method != http.MethodPost && c.Request.Method != http.MethodPatch {
			c.Next()

			return
		}

		body, err := readAndRestoreBody(c.Request)
		if err != nil {
			rejectExternalEndpoint(c, invalidExternalEndpointPayloadError(err.Error()))

			return
		}

		// An empty body is PostgREST's to reject, and it says so better than a
		// name check could.
		if len(bytes.TrimSpace(body)) == 0 {
			c.Next()

			return
		}

		payloads, err := decodeResourcePayloads(body)
		if err != nil {
			rejectExternalEndpoint(c, invalidExternalEndpointPayloadError(err.Error()))

			return
		}

		for _, payload := range payloads {
			if validationErr := validateExternalEndpointName(payload, c.Request.Method); validationErr != nil {
				rejectExternalEndpoint(c, validationErr)

				return
			}
		}

		c.Next()
	}
}

// validateExternalEndpointName checks the name a single payload carries.
//
// A POST must name the resource it creates. A PATCH is judged only on what it
// actually sends: a patch that does not touch metadata.name leaves the name
// alone, and a soft delete has to keep working even for a resource whose name
// predates this check -- otherwise a bad name would be undeletable through the
// API.
func validateExternalEndpointName(payload map[string]json.RawMessage, method string) *validationError {
	metadataRaw, hasMetadata := payload["metadata"]

	var metadata map[string]json.RawMessage

	if hasMetadata {
		if err := json.Unmarshal(metadataRaw, &metadata); err != nil {
			return invalidExternalEndpointPayloadError(err.Error())
		}
	}

	if method == http.MethodPatch {
		if !hasMetadata || isSoftDeleteMetadata(metadata) {
			return nil
		}

		if _, ok := metadata["name"]; !ok {
			return nil
		}
	}

	var name string

	if nameRaw, ok := metadata["name"]; ok {
		if err := json.Unmarshal(nameRaw, &name); err != nil {
			return invalidExternalEndpointPayloadError(err.Error())
		}
	}

	if err := v1.ValidateResourceName(externalEndpointKind, name); err != nil {
		return invalidExternalEndpointNameError(err.Error())
	}

	return nil
}

func isSoftDeleteMetadata(metadata map[string]json.RawMessage) bool {
	raw, ok := metadata["deletion_timestamp"]
	if !ok {
		return false
	}

	var deletionTimestamp string
	if err := json.Unmarshal(raw, &deletionTimestamp); err != nil {
		return false
	}

	return deletionTimestamp != ""
}

// decodeResourcePayloads accepts both shapes PostgREST takes: a single resource
// object and an array of them.
func decodeResourcePayloads(body []byte) ([]map[string]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var payloads []map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &payloads); err != nil {
			return nil, err
		}

		return payloads, nil
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &payload); err != nil {
		return nil, err
	}

	return []map[string]json.RawMessage{payload}, nil
}

// readAndRestoreBody consumes the request body and puts it back, so the proxy
// handler downstream still sees an unread body of the declared length.
func readAndRestoreBody(r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}

	if err := r.Body.Close(); err != nil {
		return nil, err
	}

	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.Header.Set("Content-Length", strconv.Itoa(len(body)))

	return body, nil
}

func rejectExternalEndpoint(c *gin.Context, validationErr *validationError) {
	c.JSON(validationErrStatus(validationErr), validationErr)
	c.Abort()
}

func invalidExternalEndpointPayloadError(hint string) *validationError {
	return &validationError{
		Code:    externalEndpointInvalidPayloadCode,
		Message: "invalid external endpoint payload",
		Hint:    hint,
	}
}

func invalidExternalEndpointNameError(hint string) *validationError {
	return &validationError{
		Code:    externalEndpointInvalidNameCode,
		Message: "invalid external endpoint name",
		Hint:    hint + "; use metadata.display_name for a human-readable name",
	}
}
