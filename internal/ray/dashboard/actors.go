package dashboard

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/pkg/errors"
)

// ActorFilter is a single (key, predicate, value) tuple sent to Ray's
// state API as repeated filter_keys / filter_predicates / filter_values
// query parameters.
//
// Predicate must be "=" or "!="; only "=" is pushed down to GCS for the
// supported keys (actor_id / state / job_id). All other filters fall
// back to client-side do_filter inside the dashboard process.
type ActorFilter struct {
	Key       string
	Predicate string
	Value     string
}

// ActorsResponse is the full envelope returned by GET /api/v0/actors,
// after Ray's rest_response + do_reply wraps the inner ListApiResponse.
type ActorsResponse struct {
	Result bool               `json:"result"`
	Msg    string             `json:"msg"`
	Data   ActorsResponseData `json:"data"`
}

type ActorsResponseData struct {
	Result ActorsListResult `json:"result"`
}

type ActorsListResult struct {
	Total              int     `json:"total"`
	NumAfterTruncation int     `json:"num_after_truncation"`
	NumFiltered        int     `json:"num_filtered"`
	Result             []Actor `json:"result"`
}

// Actor mirrors fields exposed by Ray's state API for actors. Field names
// follow the snake_case used by Ray (preserving_proto_field_name=True).
//
// StartTime / EndTime come from the underlying GCS ActorTableData protobuf
// (start_time / end_time, unix ms) — they pass through the JSON even though
// they are not listed in the public ActorState docstring. They are required
// to identify the most recently created actor for a Serve deployment after
// Ray Serve has removed the failed replica from /api/serve/applications/.
//
// DeathCause only populates when detail=true is sent in the request.
type Actor struct {
	ActorID    string                 `json:"actor_id"`
	ClassName  string                 `json:"class_name"`
	State      string                 `json:"state"`
	Name       string                 `json:"name"`
	NodeID     string                 `json:"node_id"`
	PID        int                    `json:"pid"`
	StartTime  int64                  `json:"start_time"`
	EndTime    int64                  `json:"end_time"`
	DeathCause map[string]interface{} `json:"death_cause,omitempty"`
}

// ListActors queries GET /api/v0/actors with the given filters.
//
// detail=true asks Ray to populate detail-only fields such as DeathCause.
// limit > 0 caps the response; pass 0 to use Ray's default (100).
func (c *Client) ListActors(filters []ActorFilter, detail bool, limit int) (*ActorsResponse, error) {
	q := url.Values{}
	for _, f := range filters {
		q.Add("filter_keys", f.Key)
		q.Add("filter_predicates", f.Predicate)
		q.Add("filter_values", f.Value)
	}

	if detail {
		q.Set("detail", "true")
	}

	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}

	path := "/api/v0/actors"
	if encoded := q.Encode(); encoded != "" {
		path += "?" + encoded
	}

	var resp ActorsResponse
	if err := c.doRequest(http.MethodGet, path, nil, &resp); err != nil {
		return nil, errors.Wrap(err, "failed to list actors")
	}

	return &resp, nil
}
