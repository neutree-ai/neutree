package dashboard

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/pkg/errors"
)

// GetActorLog fetches a Ray actor stdout/stderr tail from the dashboard logs API.
func (c *Client) GetActorLog(actorID, suffix string, lines int) (string, error) {
	q := url.Values{}
	q.Set("actor_id", actorID)
	q.Set("suffix", suffix)
	q.Set("format", "text")

	if lines > 0 {
		q.Set("lines", strconv.Itoa(lines))
	}

	req, err := http.NewRequest(http.MethodGet, c.dashboardURL+"/api/v0/logs/file?"+q.Encode(), nil)
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

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", errors.Wrap(err, "failed to read actor log response")
	}

	return string(data), nil
}
