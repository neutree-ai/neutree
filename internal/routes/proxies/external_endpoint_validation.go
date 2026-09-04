package proxies

import (
	"bytes"
	"encoding/json"
	"fmt"
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
	externalEndpointInvalidRoutingCode = "10233"
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

			if validationErr := validateExternalEndpointRouting(payload); validationErr != nil {
				rejectExternalEndpoint(c, validationErr)

				return
			}
		}

		c.Next()
	}
}

func validateExternalEndpointRouting(payload map[string]json.RawMessage) *validationError {
	specRaw, ok := payload["spec"]
	if !ok || bytes.Equal(bytes.TrimSpace(specRaw), []byte("null")) {
		return nil
	}

	var spec map[string]json.RawMessage
	if err := json.Unmarshal(specRaw, &spec); err != nil {
		return invalidExternalEndpointPayloadError(err.Error())
	}

	upstreamsRaw, ok := spec["upstreams"]
	if !ok || bytes.Equal(bytes.TrimSpace(upstreamsRaw), []byte("null")) {
		return nil
	}

	var upstreams []map[string]json.RawMessage
	if err := json.Unmarshal(upstreamsRaw, &upstreams); err != nil {
		return invalidExternalEndpointPayloadError(err.Error())
	}

	modelRoutesRaw, ok := spec["model_routes"]
	if !ok || bytes.Equal(bytes.TrimSpace(modelRoutesRaw), []byte("null")) {
		return nil
	}

	var modelRoutes []struct {
		Model    string `json:"model"`
		Strategy string `json:"strategy"`
		Targets  []struct {
			Upstream      string          `json:"upstream"`
			UpstreamModel string          `json:"upstream_model"`
			Priority      json.RawMessage `json:"priority"`
			Weight        json.RawMessage `json:"weight"`
			MaxInflight   json.RawMessage `json:"max_inflight_requests"`
		} `json:"targets"`
	}
	if err := json.Unmarshal(modelRoutesRaw, &modelRoutes); err != nil {
		return invalidExternalEndpointPayloadError(err.Error())
	}
	providerNames := make(map[string]struct{}, len(upstreams))
	for i, upstream := range upstreams {
		nameRaw, hasName := upstream["name"]
		if !hasName {
			return invalidExternalEndpointRoutingError(i, "name is required when model_routes is configured")
		}
		var name string
		if err := json.Unmarshal(nameRaw, &name); err != nil || name == "" {
			return invalidExternalEndpointRoutingError(i, "name must be a non-empty string when model_routes is configured")
		}
		if _, exists := providerNames[name]; exists {
			return invalidExternalEndpointRoutingError(i, fmt.Sprintf("duplicate upstream name %q", name))
		}
		providerNames[name] = struct{}{}
	}
	seenModels := make(map[string]struct{}, len(modelRoutes))
	for i, route := range modelRoutes {
		if route.Model == "" {
			return invalidExternalEndpointRoutingError(i, "model must not be empty")
		}
		if len(route.Targets) == 0 {
			return invalidExternalEndpointRoutingError(i, "targets must not be empty")
		}
		if route.Strategy != "" && route.Strategy != "weighted_random" {
			return invalidExternalEndpointRoutingError(i, fmt.Sprintf("unsupported strategy %q", route.Strategy))
		}
		if _, exists := seenModels[route.Model]; exists {
			return invalidExternalEndpointRoutingError(i, fmt.Sprintf("duplicate model route %q", route.Model))
		}
		seenModels[route.Model] = struct{}{}
		for j, target := range route.Targets {
			if target.Upstream == "" || target.UpstreamModel == "" {
				return invalidExternalEndpointRoutingError(i, fmt.Sprintf("targets[%d] upstream and upstream_model are required", j))
			}
			if _, exists := providerNames[target.Upstream]; !exists {
				return invalidExternalEndpointRoutingError(i, fmt.Sprintf("targets[%d] references unknown upstream %q", j, target.Upstream))
			}
			for field, raw := range map[string]json.RawMessage{
				"priority":              target.Priority,
				"weight":                target.Weight,
				"max_inflight_requests": target.MaxInflight,
			} {
				if len(raw) == 0 {
					continue
				}
				minimum, strict := 0, false
				if field == "weight" {
					strict = true
				}
				if err := validateOptionalRoutingInteger(map[string]json.RawMessage{field: raw}, field, minimum, strict); err != nil {
					return invalidExternalEndpointRoutingError(i, fmt.Sprintf("targets[%d] %s", j, err.Error()))
				}
			}
		}
	}

	return nil
}

func validateOptionalRoutingInteger(target map[string]json.RawMessage, field string, minimum int,
	strictMinimum bool) error {
	raw, ok := target[field]
	if !ok {
		return nil
	}

	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("%s must be an integer", field)
	}

	var value int
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("%s must be an integer", field)
	}

	if strictMinimum && value <= minimum {
		return fmt.Errorf("%s must be greater than %d", field, minimum)
	}
	if !strictMinimum && value < minimum {
		return fmt.Errorf("%s must be greater than or equal to %d", field, minimum)
	}

	return nil
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

func invalidExternalEndpointRoutingError(index int, hint string) *validationError {
	return &validationError{
		Code:    externalEndpointInvalidRoutingCode,
		Message: "invalid external endpoint routing policy",
		Hint:    fmt.Sprintf("spec.upstreams[%d].%s", index, hint),
	}
}
