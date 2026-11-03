package middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

const (
	sessionTestSecret  = "test-secret-32bytes-for-aes256!!"
	sessionTestUserID  = "12345678-1234-1234-1234-123456789abc"
	sessionTestSession = "0b8f4a52-7c1e-4d3e-9f6a-1a2b3c4d5e6f"
)

func sessionToken(t *testing.T, sessionID string) string {
	t.Helper()

	claims := &Claims{
		UserID:    sessionTestUserID,
		SessionID: sessionID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}

	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(sessionTestSecret))
	require.NoError(t, err)

	return "Bearer " + signed
}

func serveAuth(t *testing.T, handler gin.HandlerFunc, authHeader string) int {
	t.Helper()

	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, engine := gin.CreateTestContext(w)
	engine.GET("/", handler, func(c *gin.Context) { c.Status(http.StatusOK) })

	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	c.Request.Header.Set("Authorization", authHeader)
	engine.HandleContext(c)

	return w.Code
}

func TestAuth_ActiveSessionIsCheckedOncePerTTL(t *testing.T) {
	store := storagemocks.NewMockStorage(t)
	store.EXPECT().IsSessionActive(sessionTestSession, sessionTestUserID).Return(true, nil).Once()

	handler := Auth(Dependencies{Config: AuthConfig{JwtSecret: sessionTestSecret}, Storage: store})
	token := sessionToken(t, sessionTestSession)

	assert.Equal(t, http.StatusOK, serveAuth(t, handler, token))
	assert.Equal(t, http.StatusOK, serveAuth(t, handler, token), "the second request uses the cached check")
}

func TestAuth_EndedSessionIs401(t *testing.T) {
	store := storagemocks.NewMockStorage(t)
	store.EXPECT().IsSessionActive(sessionTestSession, sessionTestUserID).Return(false, nil).Once()

	handler := Auth(Dependencies{Config: AuthConfig{JwtSecret: sessionTestSecret}, Storage: store})

	assert.Equal(t, http.StatusUnauthorized, serveAuth(t, handler, sessionToken(t, sessionTestSession)))
}

// A check that fails denies the request with 500 (not 401, which would end
// the client's session) and is not cached.
func TestAuth_SessionCheckFailureFailsClosed(t *testing.T) {
	store := storagemocks.NewMockStorage(t)
	store.EXPECT().IsSessionActive(sessionTestSession, sessionTestUserID).Return(false, errors.New("postgrest down")).Once()
	store.EXPECT().IsSessionActive(sessionTestSession, sessionTestUserID).Return(true, nil).Once()

	handler := Auth(Dependencies{Config: AuthConfig{JwtSecret: sessionTestSecret}, Storage: store})
	token := sessionToken(t, sessionTestSession)

	assert.Equal(t, http.StatusInternalServerError, serveAuth(t, handler, token))
	assert.Equal(t, http.StatusOK, serveAuth(t, handler, token))
}

// A token without a session_id claim is not a GoTrue session token; it is not
// checked.
func TestAuth_TokenWithoutSessionIsNotChecked(t *testing.T) {
	store := storagemocks.NewMockStorage(t)

	handler := Auth(Dependencies{Config: AuthConfig{JwtSecret: sessionTestSecret}, Storage: store})

	assert.Equal(t, http.StatusOK, serveAuth(t, handler, sessionToken(t, "")))
	store.AssertNotCalled(t, "IsSessionActive")
}

func TestAuth_APIKeyIsNotSessionChecked(t *testing.T) {
	const keyID = "87654321-4321-4321-4321-abcdef123456"

	store := storagemocks.NewMockStorage(t)
	store.EXPECT().GetApiKey(keyID).Return(&v1.ApiKey{ID: keyID, Metadata: &v1.Metadata{Name: "k"}, Spec: &v1.ApiKeySpec{}}, nil).Once()

	key, err := createTestAPIKey(sessionTestUserID, keyID, sessionTestSecret, 0)
	require.NoError(t, err)

	handler := Auth(Dependencies{Config: AuthConfig{JwtSecret: sessionTestSecret}, Storage: store})

	assert.Equal(t, http.StatusOK, serveAuth(t, handler, key))
	store.AssertNotCalled(t, "IsSessionActive")
}

func TestSessionCache_Expires(t *testing.T) {
	now := time.Unix(1000, 0)
	cache := newSessionCache(10*time.Second, func() time.Time { return now })

	cache.put("k", true)

	active, ok := cache.get("k")
	assert.True(t, ok)
	assert.True(t, active)

	now = now.Add(10 * time.Second)

	_, ok = cache.get("k")
	assert.False(t, ok, "an entry is not used at or after its expiry")
}
