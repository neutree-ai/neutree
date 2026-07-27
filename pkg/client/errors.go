package client

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// APIError is returned when the API responds with an unexpected status code.
// It preserves the status code so callers can branch on it — e.g. treat 403 on
// the credentials endpoint as "credentials not available" rather than a hard
// failure — via HasStatus, without matching on the message text.
type APIError struct {
	StatusCode int
	Body       string

	// Expected lists the status codes the caller accepted.
	Expected []int
}

func (e *APIError) Error() string {
	return fmt.Sprintf("server returned non-%s status: %d, body: %s",
		joinStatuses(e.Expected), e.StatusCode, e.Body)
}

// expectStatus returns an *APIError unless resp carries one of the allowed
// status codes, draining the body into the error so callers can report it.
func expectStatus(resp *http.Response, allowed ...int) error {
	for _, code := range allowed {
		if resp.StatusCode == code {
			return nil
		}
	}

	body, _ := io.ReadAll(resp.Body)

	return &APIError{StatusCode: resp.StatusCode, Body: string(body), Expected: allowed}
}

// HasStatus reports whether err is an *APIError carrying one of codes. It lets
// callers act on the transport status without knowing how errors are formatted.
func HasStatus(err error, codes ...int) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}

	for _, code := range codes {
		if apiErr.StatusCode == code {
			return true
		}
	}

	return false
}

func joinStatuses(codes []int) string {
	parts := make([]string, 0, len(codes))
	for _, code := range codes {
		parts = append(parts, strconv.Itoa(code))
	}

	return strings.Join(parts, "/")
}
