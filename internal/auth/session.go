package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// sessionRequestTimeout bounds each GoTrue call made while issuing a session.
const sessionRequestTimeout = 10 * time.Second

// maxGoTrueResponseSize caps how much of a GoTrue response is read.
const maxGoTrueResponseSize = 1 << 20

// MagicLink is the part of a GoTrue admin generate_link response needed to
// turn it into a session.
type MagicLink struct {
	// UserID is the user GoTrue issued the link for. For a magic link GoTrue
	// signs up a new user when the email is unknown, so callers must compare it
	// with the user they expected.
	UserID      string
	HashedToken string
}

// SessionIssuer mints GoTrue sessions for users neutree has authenticated
// itself, e.g. against an LDAP directory, without knowing their GoTrue password.
type SessionIssuer interface {
	// GenerateMagicLink asks GoTrue for a magic link token for email through the
	// admin API. No mail is sent.
	GenerateMagicLink(ctx context.Context, email string) (*MagicLink, error)
	// VerifyMagicLink exchanges a magic link's hashed token for a session and
	// returns GoTrue's session JSON as is, the same body a password grant on
	// /token returns.
	VerifyMagicLink(ctx context.Context, hashedToken string) ([]byte, error)
}

type goTrueSessionIssuer struct {
	endpoint     string
	serviceToken string
	httpClient   *http.Client
}

// NewSessionIssuer returns a SessionIssuer talking to the GoTrue at endpoint.
// serviceToken must carry the service_role, which the admin API requires.
func NewSessionIssuer(endpoint, serviceToken string) SessionIssuer {
	return &goTrueSessionIssuer{
		endpoint:     strings.TrimRight(endpoint, "/"),
		serviceToken: serviceToken,
		httpClient:   &http.Client{Timeout: sessionRequestTimeout},
	}
}

func (g *goTrueSessionIssuer) GenerateMagicLink(ctx context.Context, email string) (*MagicLink, error) {
	body, err := g.post(ctx, "/admin/generate_link", true, map[string]string{
		"type":  "magiclink",
		"email": email,
	})
	if err != nil {
		return nil, err
	}

	var resp struct {
		ID          string `json:"id"`
		HashedToken string `json:"hashed_token"`
	}

	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode generate_link response: %w", err)
	}

	if resp.ID == "" || resp.HashedToken == "" {
		return nil, fmt.Errorf("generate_link response has no user id or hashed token")
	}

	return &MagicLink{UserID: resp.ID, HashedToken: resp.HashedToken}, nil
}

func (g *goTrueSessionIssuer) VerifyMagicLink(ctx context.Context, hashedToken string) ([]byte, error) {
	return g.post(ctx, "/verify", false, map[string]string{
		"type":       "magiclink",
		"token_hash": hashedToken,
	})
}

// post sends a JSON body to GoTrue and returns the response body of a 200.
// The service token is sent only to admin endpoints.
func (g *goTrueSessionIssuer) post(ctx context.Context, path string, admin bool, payload any) ([]byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode %s request: %w", path, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.endpoint+path, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("build %s request: %w", path, err)
	}

	req.Header.Set("Content-Type", "application/json")

	if admin {
		req.Header.Set("Authorization", "Bearer "+g.serviceToken)
	}

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxGoTrueResponseSize))
	if err != nil {
		return nil, fmt.Errorf("read %s response: %w", path, err)
	}

	if resp.StatusCode != http.StatusOK {
		// The body is GoTrue's error JSON; it holds no token on failure.
		return nil, fmt.Errorf("POST %s: HTTP %d: %s", path, resp.StatusCode, body)
	}

	return body, nil
}
