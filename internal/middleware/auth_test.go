package middleware

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v4"
	"github.com/stretchr/testify/assert"
)

// Helper function to create a test self-contained API key
func createTestAPIKey(userID, keyID, secret string, expiresAt int64) (string, error) {
	// Create 40-byte payload: user_id (16) + key_id (16) + expires_at (8)
	userIDBytes := make([]byte, 16)
	keyIDBytes := make([]byte, 16)
	expiresAtBytes := make([]byte, 8)

	// Parse UUIDs (remove hyphens and convert to bytes)
	userIDHex := strings.ReplaceAll(userID, "-", "")
	keyIDHex := strings.ReplaceAll(keyID, "-", "")

	// Convert hex to bytes
	for i := 0; i < 16; i++ {
		if i*2 < len(userIDHex) {
			userIDBytes[i] = byte((hexToByte(userIDHex[i*2]) << 4) | hexToByte(userIDHex[i*2+1]))
		}
		if i*2 < len(keyIDHex) {
			keyIDBytes[i] = byte((hexToByte(keyIDHex[i*2]) << 4) | hexToByte(keyIDHex[i*2+1]))
		}
	}

	// Convert expires_at to big-endian 8 bytes
	for i := 7; i >= 0; i-- {
		expiresAtBytes[i] = byte(expiresAt & 0xFF)
		expiresAt >>= 8
	}

	// Combine payload
	payload := make([]byte, 40)
	copy(payload[0:16], userIDBytes)
	copy(payload[16:32], keyIDBytes)
	copy(payload[32:40], expiresAtBytes)

	// Add PKCS#7 padding to make it 48 bytes (3 AES blocks)
	paddingLen := 8
	paddedPayload := make([]byte, 48)
	copy(paddedPayload, payload)
	for i := 40; i < 48; i++ {
		paddedPayload[i] = byte(paddingLen)
	}

	// Encrypt with AES-256-CBC (32-byte key)
	key := make([]byte, 32)
	copy(key, []byte(secret))

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	iv := make([]byte, aes.BlockSize) // All zeros
	mode := cipher.NewCBCEncrypter(block, iv)

	encrypted := make([]byte, 48)
	mode.CryptBlocks(encrypted, paddedPayload)

	// Create HMAC signature (truncated to 16 bytes)
	h := hmac.New(sha256.New, []byte(secret))
	h.Write(encrypted)
	signature := h.Sum(nil)[:16]

	// Combine encrypted data and signature
	combined := append(encrypted, signature...)

	// Convert to base64url
	b64 := base64.StdEncoding.EncodeToString(combined)
	b64url := strings.ReplaceAll(strings.ReplaceAll(b64, "+", "-"), "/", "_")
	b64url = strings.TrimRight(b64url, "=")

	return "sk_" + b64url, nil
}

func hexToByte(c byte) byte {
	if c >= '0' && c <= '9' {
		return c - '0'
	}
	if c >= 'a' && c <= 'f' {
		return c - 'a' + 10
	}
	if c >= 'A' && c <= 'F' {
		return c - 'A' + 10
	}
	return 0
}

func TestAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)

	jwtSecret := "test-secret-32bytes-for-aes256!!"
	config := AuthConfig{
		JwtSecret: jwtSecret,
	}

	tests := []struct {
		name                  string
		setupAuth             func() string
		expectedStatus        int
		expectUserID          string
		expectPostgrestToken  bool
	}{
		{
			name: "Valid JWT token",
			setupAuth: func() string {
				claims := &Claims{
					UserID: "user-123",
					RegisteredClaims: jwt.RegisteredClaims{
						ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
						IssuedAt:  jwt.NewNumericDate(time.Now()),
					},
				}

				token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
				tokenString, _ := token.SignedString([]byte(jwtSecret))
				return "Bearer " + tokenString
			},
			expectedStatus:       http.StatusOK,
			expectUserID:         "user-123",
			expectPostgrestToken: false, // JWT tokens don't get postgrest_token
		},
		{
			name: "Valid self-contained API key",
			setupAuth: func() string {
				apiKey, _ := createTestAPIKey(
					"12345678-1234-1234-1234-123456789abc",
					"87654321-4321-4321-4321-abcdef123456",
					jwtSecret,
					0, // Never expires
				)
				return apiKey
			},
			expectedStatus:       http.StatusOK,
			expectUserID:         "12345678-1234-1234-1234-123456789abc",
			expectPostgrestToken: true, // API keys should generate postgrest_token
		},
		{
			name: "Invalid API Key format",
			setupAuth: func() string {
				return "sk_invalid_format"
			},
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name: "Missing Authorization header",
			setupAuth: func() string {
				return ""
			},
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name: "Invalid Authorization header format",
			setupAuth: func() string {
				return "Invalid header"
			},
			expectedStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := gin.New()
			r.Use(Auth(Dependencies{
				Config: config,
			}))
			r.GET("/test", func(c *gin.Context) {
				userID, _ := GetUserID(c)
				postgrestToken, hasPostgrestToken := GetPostgrestToken(c)
				c.JSON(http.StatusOK, gin.H{
					"user_id":               userID,
					"has_postgrest_token":   hasPostgrestToken,
					"postgrest_token_empty": postgrestToken == "",
				})
			})

			req := httptest.NewRequest("GET", "/test", nil)

			authHeader := tt.setupAuth()
			if authHeader != "" {
				req.Header.Set("Authorization", authHeader)
			}

			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)

			if tt.expectedStatus == http.StatusOK && tt.expectUserID != "" {
				var response map[string]interface{}
				err := json.Unmarshal(w.Body.Bytes(), &response)
				assert.NoError(t, err)
				assert.Equal(t, tt.expectUserID, response["user_id"])

				// Verify postgrest_token behavior
				if tt.expectPostgrestToken {
					assert.True(t, response["has_postgrest_token"].(bool), "API key auth should have postgrest_token")
					assert.False(t, response["postgrest_token_empty"].(bool), "postgrest_token should not be empty")
				} else {
					assert.False(t, response["has_postgrest_token"].(bool), "JWT auth should not have postgrest_token")
				}
			}
		})
	}
}

func TestParseBearerToken(t *testing.T) {
	jwtSecret := "test-secret-32bytes-for-aes256!!"
	config := AuthConfig{
		JwtSecret: jwtSecret,
	}

	tests := []struct {
		name        string
		authHeader  string
		setupToken  func() string
		expectError bool
		expectID    string
	}{
		{
			name: "Valid bearer token",
			setupToken: func() string {
				claims := &Claims{
					UserID: "user-123",
					RegisteredClaims: jwt.RegisteredClaims{
						ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
						IssuedAt:  jwt.NewNumericDate(time.Now()),
					},
				}
				token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
				tokenString, _ := token.SignedString([]byte(jwtSecret))
				return "Bearer " + tokenString
			},
			expectError: false,
			expectID:    "user-123",
		},
		{
			name:        "Empty token",
			authHeader:  "Bearer ",
			expectError: true,
		},
		{
			name:        "Invalid token",
			authHeader:  "Bearer invalid-token",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			authHeader := tt.authHeader
			if tt.setupToken != nil {
				authHeader = tt.setupToken()
			}

			parsedInfo, err := parseBearerToken(config, authHeader)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, parsedInfo)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, parsedInfo)
				assert.Equal(t, tt.expectID, parsedInfo.UserID)
			}
		})
	}
}

func TestParseSelfContainedAPIKey(t *testing.T) {
	jwtSecret := "test-secret-32bytes-for-aes256!!"

	tests := []struct {
		name         string
		setupAPIKey  func() string
		secret       string
		expectError  bool
		expectUserID string
		expectKeyID  string
	}{
		{
			name: "Valid API key",
			setupAPIKey: func() string {
				apiKey, _ := createTestAPIKey(
					"12345678-1234-1234-1234-123456789abc",
					"87654321-4321-4321-4321-abcdef123456",
					jwtSecret,
					0,
				)
				return apiKey
			},
			secret:       jwtSecret,
			expectError:  false,
			expectUserID: "12345678-1234-1234-1234-123456789abc",
			expectKeyID:  "87654321-4321-4321-4321-abcdef123456",
		},
		{
			name: "Valid API key with expiration",
			setupAPIKey: func() string {
				// Set expiration to far future
				apiKey, _ := createTestAPIKey(
					"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
					"11111111-2222-3333-4444-555555555555",
					jwtSecret,
					time.Now().Add(24*time.Hour).Unix(),
				)
				return apiKey
			},
			secret:       jwtSecret,
			expectError:  false,
			expectUserID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
			expectKeyID:  "11111111-2222-3333-4444-555555555555",
		},
		{
			name: "Invalid prefix",
			setupAPIKey: func() string {
				return "invalid_prefix_12345"
			},
			secret:      jwtSecret,
			expectError: true,
		},
		{
			name: "Invalid base64",
			setupAPIKey: func() string {
				return "sk_invalid-base64-characters!"
			},
			secret:      jwtSecret,
			expectError: true,
		},
		{
			name: "Empty secret",
			setupAPIKey: func() string {
				apiKey, _ := createTestAPIKey(
					"12345678-1234-1234-1234-123456789abc",
					"87654321-4321-4321-4321-abcdef123456",
					jwtSecret,
					0,
				)
				return apiKey
			},
			secret:      "", // Empty secret
			expectError: true,
		},
		{
			name: "Wrong secret",
			setupAPIKey: func() string {
				apiKey, _ := createTestAPIKey(
					"12345678-1234-1234-1234-123456789abc",
					"87654321-4321-4321-4321-abcdef123456",
					jwtSecret,
					0,
				)
				return apiKey
			},
			secret:      "wrong-secret",
			expectError: true,
		},
		{
			name: "Too short data",
			setupAPIKey: func() string {
				return "sk_dGVzdA"
			},
			secret:      jwtSecret,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			apiKey := tt.setupAPIKey()
			payload, err := parseSelfContainedAPIKey(apiKey, tt.secret)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, payload)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, payload)
				assert.Equal(t, tt.expectUserID, payload.UserID)
				assert.Equal(t, tt.expectKeyID, payload.KeyID)
			}
		})
	}
}

func TestDecryptAESPayload(t *testing.T) {
	tests := []struct {
		name        string
		secret      string
		expectError bool
	}{
		{
			name:        "AES-256 key (32 bytes)",
			secret:      "12345678901234567890123456789012",
			expectError: false,
		},
		{
			name:        "Short key (padded to 16)",
			secret:      "short",
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test payload
			payload := make([]byte, 40)
			for i := 0; i < 40; i++ {
				payload[i] = byte(i)
			}

			// Add PKCS#7 padding
			paddingLen := 8
			paddedPayload := make([]byte, 48)
			copy(paddedPayload, payload)
			for i := 40; i < 48; i++ {
				paddedPayload[i] = byte(paddingLen)
			}

			// Encrypt with the same logic as our implementation
			key := []byte(tt.secret)
			var finalKey []byte
			if len(key) <= 16 {
				finalKey = make([]byte, 16)
				copy(finalKey, key)
			} else if len(key) <= 24 {
				finalKey = make([]byte, 24)
				copy(finalKey, key)
			} else {
				finalKey = make([]byte, 32)
				copy(finalKey, key)
			}

			block, err := aes.NewCipher(finalKey)
			assert.NoError(t, err)

			iv := make([]byte, aes.BlockSize)
			mode := cipher.NewCBCEncrypter(block, iv)

			encrypted := make([]byte, 48)
			mode.CryptBlocks(encrypted, paddedPayload)

			// Test decryption
			decrypted, err := decryptAESPayload(encrypted, tt.secret)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, payload, decrypted)
			}
		})
	}
}

func TestGetUserID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name         string
		setupContext func(*gin.Context)
		expectedID   string
		expectedOK   bool
	}{
		{
			name: "Valid user ID in context",
			setupContext: func(c *gin.Context) {
				c.Set("user_id", "user-123")
			},
			expectedID: "user-123",
			expectedOK: true,
		},
		{
			name: "No user ID in context",
			setupContext: func(c *gin.Context) {
				// Don't set anything
			},
			expectedID: "",
			expectedOK: false,
		},
		{
			name: "Invalid type in context",
			setupContext: func(c *gin.Context) {
				c.Set("user_id", 123) // Wrong type
			},
			expectedID: "",
			expectedOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			tt.setupContext(c)

			userID, ok := GetUserID(c)
			assert.Equal(t, tt.expectedID, userID)
			assert.Equal(t, tt.expectedOK, ok)
		})
	}
}

func TestGeneratePostgrestToken(t *testing.T) {
	jwtSecret := "test-secret-32bytes-for-aes256!!"

	tests := []struct {
		name        string
		userID      string
		secret      string
		expectError bool
	}{
		{
			name:        "Valid token generation",
			userID:      "12345678-1234-1234-1234-123456789abc",
			secret:      jwtSecret,
			expectError: false,
		},
		{
			name:        "Valid token with different user ID",
			userID:      "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
			secret:      jwtSecret,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token, err := GeneratePostgrestToken(tt.userID, tt.secret)

			if tt.expectError {
				assert.Error(t, err)
				assert.Empty(t, token)
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, token)

				// Verify the token can be parsed and contains correct claims
				parsedToken, err := jwt.Parse(token, func(token *jwt.Token) (interface{}, error) {
					return []byte(tt.secret), nil
				})
				assert.NoError(t, err)
				assert.True(t, parsedToken.Valid)

				claims, ok := parsedToken.Claims.(jwt.MapClaims)
				assert.True(t, ok)
				assert.Equal(t, tt.userID, claims["sub"])
				assert.Equal(t, "api_user", claims["role"])
			}
		})
	}
}

func TestGetPostgrestToken(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name          string
		setupContext  func(*gin.Context)
		expectedToken string
		expectedOK    bool
	}{
		{
			name: "Valid postgrest token in context",
			setupContext: func(c *gin.Context) {
				c.Set("postgrest_token", "test-token-123")
			},
			expectedToken: "test-token-123",
			expectedOK:    true,
		},
		{
			name: "No postgrest token in context",
			setupContext: func(c *gin.Context) {
				// Don't set anything
			},
			expectedToken: "",
			expectedOK:    false,
		},
		{
			name: "Invalid type in context",
			setupContext: func(c *gin.Context) {
				c.Set("postgrest_token", 123) // Wrong type
			},
			expectedToken: "",
			expectedOK:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			tt.setupContext(c)

			token, ok := GetPostgrestToken(c)
			assert.Equal(t, tt.expectedToken, token)
			assert.Equal(t, tt.expectedOK, ok)
		})
	}
}
