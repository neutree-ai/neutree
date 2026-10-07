package auth

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/neutree-ai/neutree/internal/middleware"
)

const testJWTSecret = "test-jwt-secret"

// closeNotifyRecorder lets httputil.ReverseProxy run against a recorder.
type closeNotifyRecorder struct {
	*httptest.ResponseRecorder
}

func (r *closeNotifyRecorder) CloseNotify() <-chan bool {
	return make(chan bool)
}

func signTestToken(t *testing.T, secret string, appMetadata map[string]any) string {
	t.Helper()

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":          "6a1e2f3c-0000-4000-8000-000000000001",
		"app_metadata": appMetadata,
	})

	signed, err := token.SignedString([]byte(secret))
	require.NoError(t, err)

	return signed
}

// serveUpdateUser sends PUT /api/v1/auth/user through the auth routes to a fake
// GoTrue. It returns the response and the body GoTrue received, or nil when the
// request was not proxied.
func serveUpdateUser(t *testing.T, authHeader, body string) (*httptest.ResponseRecorder, []byte) {
	t.Helper()

	gin.SetMode(gin.TestMode)

	var proxied []byte

	gotrue := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		assert.Equal(t, "/user", r.URL.Path)

		proxied, _ = io.ReadAll(r.Body)
		if proxied == nil {
			proxied = []byte{}
		}

		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(gotrue.Close)

	engine := gin.New()
	RegisterAuthRoutes(engine.Group("/api/v1"), nil, &Dependencies{
		AuthEndpoint: gotrue.URL,
		AuthConfig:   middleware.AuthConfig{JwtSecret: testJWTSecret},
	})

	w := &closeNotifyRecorder{httptest.NewRecorder()}
	req := httptest.NewRequest(http.MethodPut, "/api/v1/auth/user", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}

	engine.ServeHTTP(w, req)

	return w.ResponseRecorder, proxied
}

func TestUpdateUser_IdentitySourcePolicy(t *testing.T) {
	ldapToken := "Bearer " + signTestToken(t, testJWTSecret, map[string]any{"provider": "email", identitySourceKey: ldapSource})
	localToken := "Bearer " + signTestToken(t, testJWTSecret, map[string]any{"provider": "email"})
	forgedToken := "Bearer " + signTestToken(t, "another-secret", map[string]any{identitySourceKey: ldapSource})

	cases := []struct {
		name        string
		authHeader  string
		body        string
		wantStatus  int
		wantProxied bool
	}{
		{"ldap user password", ldapToken, `{"password":"new-password"}`, http.StatusForbidden, false},
		{"ldap user email", ldapToken, `{"email":"alice@example.org"}`, http.StatusForbidden, false},
		{"ldap user phone", ldapToken, `{"phone":"+15550100"}`, http.StatusForbidden, false},
		{"ldap user password key in another case", ldapToken, `{"Password":"new-password"}`, http.StatusForbidden, false},
		{"ldap user password beside metadata", ldapToken, `{"data":{"theme":"dark"},"password":"x"}`, http.StatusForbidden, false},
		{"ldap user metadata only", ldapToken, `{"data":{"theme":"dark"}}`, http.StatusOK, true},
		{"ldap user null password", ldapToken, `{"password":null,"data":{"theme":"dark"}}`, http.StatusOK, true},
		{"ldap user invalid body", ldapToken, `not json`, http.StatusBadRequest, false},
		{"local user password", localToken, `{"password":"new-password"}`, http.StatusOK, true},
		{"local user email", localToken, `{"email":"bob@example.org"}`, http.StatusOK, true},
		{"missing token", "", `{"password":"new-password"}`, http.StatusOK, true},
		{"token signed with another secret", forgedToken, `{"password":"new-password"}`, http.StatusOK, true},
		{"malformed token", "Bearer not-a-jwt", `{"password":"new-password"}`, http.StatusOK, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, proxied := serveUpdateUser(t, tc.authHeader, tc.body)

			assert.Equal(t, tc.wantStatus, w.Code)

			if tc.wantProxied {
				assert.Equal(t, tc.body, string(proxied))
			} else {
				assert.Nil(t, proxied)
			}

			if tc.wantStatus == http.StatusForbidden {
				assert.Contains(t, errorBody(t, w), "managed by the ldap identity source")
			}
		})
	}
}
