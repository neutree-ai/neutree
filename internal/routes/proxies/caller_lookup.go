package proxies

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/gin-gonic/gin"
)

// postgrestReservedParams are the PostgREST query params that do not filter rows.
var postgrestReservedParams = map[string]struct{}{
	"select":      {},
	"order":       {},
	"limit":       {},
	"offset":      {},
	"columns":     {},
	"on_conflict": {},
}

// rowFilters returns the params of query that filter rows, logic trees included.
func rowFilters(query url.Values) url.Values {
	filters := url.Values{}

	for key, values := range query {
		if _, reserved := postgrestReservedParams[key]; !reserved {
			filters[key] = values
		}
	}

	return filters
}

var callerLookupClient = &http.Client{Timeout: 30 * time.Second}

const targetLookupFailedCode = "10332"

// targetLookupError means the row a write targets could not be read. Writes
// that depend on that row must not be forwarded.
type targetLookupError struct {
	table string
	// status is PostgREST's response status, 0 when no response came back.
	status int
	err    error
}

func (e *targetLookupError) Error() string {
	if e.status != 0 {
		return fmt.Sprintf("%s lookup returned status %d", e.table, e.status)
	}

	return fmt.Sprintf("%s lookup failed: %v", e.table, e.err)
}

func (e *targetLookupError) Unwrap() error { return e.err }

// validationError keeps a client error from PostgREST (e.g. an expired token)
// as is and reports anything else as a bad gateway.
func (e *targetLookupError) validationError() *validationError {
	status := http.StatusBadGateway
	if e.status >= 400 && e.status < 500 {
		status = e.status
	}

	hint := fmt.Sprintf("the stored %s could not be read; the update was not applied", e.table)
	if e.status != 0 {
		hint = fmt.Sprintf("reading the stored %s returned status %d; the update was not applied", e.table, e.status)
	}

	return &validationError{
		Code:       targetLookupFailedCode,
		Message:    "cannot read the resource to update",
		Hint:       hint,
		HTTPStatus: status,
	}
}

// lookupAsCaller reads the rows of table matching filters with the same
// PostgREST credentials as the caller's proxied request, so row-level security
// limits it to rows the caller can see. It returns at most two rows, enough to
// tell one match from several. Errors are *targetLookupError.
func lookupAsCaller[T any](c *gin.Context, storageURL, table, selectCols string, filters url.Values) ([]T, error) {
	query := url.Values{}
	for key, values := range filters {
		query[key] = values
	}

	query.Set("select", selectCols)
	query.Set("limit", "2")

	req, err := http.NewRequestWithContext(
		c.Request.Context(), http.MethodGet, fmt.Sprintf("%s/%s?%s", storageURL, table, query.Encode()), nil,
	)
	if err != nil {
		return nil, &targetLookupError{table: table, err: err}
	}

	CreatePostgrestAuthModifier(c)(req)
	req.Header.Set("Accept", "application/json")

	resp, err := callerLookupClient.Do(req)
	if err != nil {
		return nil, &targetLookupError{table: table, err: err}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &targetLookupError{table: table, status: resp.StatusCode, err: err}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &targetLookupError{table: table, status: resp.StatusCode}
	}

	var rows []T
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, &targetLookupError{table: table, status: resp.StatusCode, err: err}
	}

	return rows, nil
}
