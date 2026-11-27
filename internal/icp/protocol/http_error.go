package protocol

import (
	"encoding/json"
	"net/http"
	"strings"
)

// HTTPError preserves the upstream diagnostic with the client's error envelope.
// Kong may supply just a message on authentication or quota rejection.
func HTTPError(client ID, status int, raw []byte) []byte {
	var upstream struct {
		Type, Message string
		Error         struct {
			Type, Code string
			Message    *string
		}
	}

	err := json.Unmarshal(raw, &upstream)
	if err == nil && client == AnthropicMessages && upstream.Type == "error" && upstream.Error.Type != "" && upstream.Error.Message != nil {
		return raw
	}

	var message string
	if upstream.Error.Message != nil {
		message = *upstream.Error.Message
	}

	if message == "" {
		message = upstream.Message
	}

	if message == "" {
		message = strings.TrimSpace(string(raw))
	}

	if message == "" {
		message = http.StatusText(status)
	}

	kind := upstream.Error.Type
	if kind == "" || client == AnthropicMessages {
		switch status {
		case http.StatusBadRequest, http.StatusUnprocessableEntity:
			kind = "invalid_request_error"
		case http.StatusUnauthorized:
			kind = "authentication_error"
		case http.StatusForbidden:
			kind = "permission_error"
		case http.StatusNotFound:
			kind = "not_found_error"
		case http.StatusTooManyRequests:
			kind = "rate_limit_error"
		default:
			kind = "api_error"
		}
	}

	detail := map[string]string{"message": message, "type": kind}

	body := map[string]any{"error": detail}
	if client == AnthropicMessages {
		body["type"] = "error"
	} else if upstream.Error.Code != "" {
		detail["code"] = upstream.Error.Code
	}

	b, _ := json.Marshal(body)

	return b
}
