package dashboard

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/pkg/errors"
)

// GetActorLogHead reads the beginning of an actor log. Startup records are
// emitted once and can fall out of a tail, so the caller supplies a byte limit
// and cancellation context instead of requesting a fixed number of lines.
func (c *Client) GetActorLogHead(ctx context.Context, actorID, suffix string, maxBytes int64) (string, error) {
	return c.getActorLog(ctx, actorID, suffix, "-1", maxBytes)
}

// GetActorLog fetches a Ray actor stdout/stderr tail from the dashboard logs API.
func (c *Client) GetActorLog(actorID, suffix string, lines int) (string, error) {
	linesParam := ""
	if lines > 0 {
		linesParam = strconv.Itoa(lines)
	}

	return c.getActorLog(context.Background(), actorID, suffix, linesParam, -1)
}

func (c *Client) getActorLog(
	ctx context.Context,
	actorID, suffix, linesParam string,
	maxBytes int64,
) (string, error) {
	q := url.Values{}
	q.Set("actor_id", actorID)
	q.Set("suffix", suffix)
	q.Set("format", "text")

	if linesParam != "" {
		q.Set("lines", linesParam)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.dashboardURL+"/api/v0/logs/file?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API request failed: %s", resp.Status)
	}

	var reader io.Reader = resp.Body
	if maxBytes >= 0 {
		reader = io.LimitReader(resp.Body, maxBytes)
	}

	data, err := io.ReadAll(reader)
	if err != nil {
		return "", errors.Wrap(err, "failed to read actor log response")
	}

	return string(data), nil
}
