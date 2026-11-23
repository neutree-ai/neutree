package proxies

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
)

// postgrestConnectionErrorAs503 turns PostgREST's answer to a database
// connection that broke mid-request into 503. libpq reports such failures
// without an SQLSTATE, and PostgREST maps an empty code to 400; callers that
// retry only server errors (Vector delivering usage records) would otherwise
// drop the request while the primary fails over.
func postgrestConnectionErrorAs503(resp *http.Response) error {
	if resp.StatusCode != http.StatusBadRequest {
		return nil
	}

	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if err != nil {
		return err
	}

	resp.Body = io.NopCloser(bytes.NewReader(body))

	var pgErr struct {
		Code *string `json:"code"`
	}

	if json.Unmarshal(body, &pgErr) != nil || pgErr.Code == nil || *pgErr.Code != "" {
		return nil
	}

	resp.StatusCode = http.StatusServiceUnavailable
	resp.Status = strconv.Itoa(http.StatusServiceUnavailable) + " " + http.StatusText(http.StatusServiceUnavailable)

	return nil
}
