package middleware

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v4"
)

type Dependencies struct {
	Config AuthConfig
}

// AuthConfig holds the configuration for JWT authentication
type AuthConfig struct {
	JwtSecret string
}

// Claims represents the JWT claims structure
type Claims struct {
	UserID string `json:"sub"`
	Email  string `json:"email,omitempty"`
	jwt.RegisteredClaims
}

type ParsedInfo struct {
	UserID string `json:"user_id"`
}

type SelfContainedPayload struct {
	UserID    string `json:"user_id"`
	KeyID     string `json:"key_id"`
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp"`
}

func Auth(deps Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Extract token from Authorization header
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "Authorization header is required",
			})
			c.Abort()

			return
		}

		var parsedInfo *ParsedInfo
		var err error

		switch {
		case strings.HasPrefix(authHeader, "Bearer "):
			parsedInfo, err = parseBearerToken(deps.Config, authHeader)
		case strings.HasPrefix(authHeader, "sk_"):
			parsedInfo, err = parseApiKey(authHeader, deps.Config)
		default:
			err = errors.New("invalid Authorization header format")
		}

		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": err.Error(),
			})
			c.Abort()

			return
		}

		// Set user information in context
		c.Set("user_id", parsedInfo.UserID)

		c.Next()
	}
}

func parseBearerToken(config AuthConfig, authHeader string) (*ParsedInfo, error) {
	// Extract the token
	tokenString := strings.TrimPrefix(authHeader, "Bearer ")
	if tokenString == "" {
		return nil, errors.New("token is required")
	}

	// Parse and validate the token
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		// Validate the signing method
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}

		return []byte(config.JwtSecret), nil
	})

	if err != nil || !token.Valid {
		return nil, errors.New("invalid token")
	}

	// Extract claims
	claims, ok := token.Claims.(*Claims)
	if !ok {
		return nil, errors.New("invalid token claims")
	}

	return &ParsedInfo{
		UserID: claims.UserID,
	}, nil
}

func parseApiKey(authHeader string, config AuthConfig) (*ParsedInfo, error) {
	payload, err := parseSelfContainedAPIKey(authHeader, config.JwtSecret)
	if err != nil {
		return nil, err
	}

	return &ParsedInfo{
		UserID: payload.UserID,
	}, nil
}

func parseSelfContainedAPIKey(apiKey string, jwtSecret string) (*SelfContainedPayload, error) {
	if !strings.HasPrefix(apiKey, "sk_") {
		return nil, errors.New("invalid API key format")
	}

	if jwtSecret == "" {
		return nil, errors.New("JWT secret not provided")
	}

	// Decode base64url (convert back from URL-safe encoding)
	b64Data := strings.ReplaceAll(strings.ReplaceAll(apiKey[3:], "-", "+"), "_", "/")
	// Add padding if needed
	for len(b64Data)%4 != 0 {
		b64Data += "="
	}

	combinedData, err := base64.StdEncoding.DecodeString(b64Data)
	if err != nil {
		return nil, fmt.Errorf("failed to decode API key data: %w", err)
	}

	// The last 16 bytes are the truncated HMAC signature
	if len(combinedData) < 16 {
		return nil, errors.New("invalid API key: too short")
	}

	encryptedData := combinedData[:len(combinedData)-16]
	signature := combinedData[len(combinedData)-16:]

	// Verify HMAC signature (compare truncated signatures)
	expectedSignature := createHMAC(encryptedData, jwtSecret)[:16]

	if !hmac.Equal(signature, expectedSignature) {
		return nil, errors.New("invalid API key signature")
	}

	// Decrypt using AES
	payloadBytes, err := decryptAESPayload(encryptedData, jwtSecret)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt payload: %w", err)
	}

	// Parse binary payload: user_id (16 bytes) + key_id (16 bytes) + expires_at (8 bytes)
	if len(payloadBytes) != 40 {
		return nil, fmt.Errorf("invalid payload length: expected 40 bytes, got %d", len(payloadBytes))
	}

	// Extract UUIDs (add hyphens back)
	userIDBytes := payloadBytes[0:16]
	keyIDBytes := payloadBytes[16:32]
	expiresAtBytes := payloadBytes[32:40]

	userID := fmt.Sprintf("%x-%x-%x-%x-%x",
		userIDBytes[0:4], userIDBytes[4:6], userIDBytes[6:8], userIDBytes[8:10], userIDBytes[10:16])
	keyID := fmt.Sprintf("%x-%x-%x-%x-%x",
		keyIDBytes[0:4], keyIDBytes[4:6], keyIDBytes[6:8], keyIDBytes[8:10], keyIDBytes[10:16])

	// Extract expires_at (8-byte big-endian timestamp)
	expiresAt := int64(0)
	for i := 0; i < 8; i++ {
		expiresAt = (expiresAt << 8) | int64(expiresAtBytes[i])
	}

	// Check expiration
	if expiresAt > 0 && time.Now().Unix() > expiresAt {
		return nil, errors.New("API key has expired")
	}

	return &SelfContainedPayload{
		UserID:    userID,
		KeyID:     keyID,
		IssuedAt:  0, // Not stored in compact format
		ExpiresAt: expiresAt,
	}, nil
}

func decryptAESPayload(encryptedData []byte, secret string) ([]byte, error) {
	// PostgreSQL's encrypt() function uses AES-CBC with PKCS padding and all-zero IV by default
	key := []byte(secret)

	// Match PostgreSQL's key length handling for AES variant selection
	// PostgreSQL uses AES-128 (16 bytes), AES-192 (24 bytes), or AES-256 (32 bytes)
	var finalKey []byte

	switch {
	case len(key) <= 16:
		// Pad to 16 bytes for AES-128
		finalKey = make([]byte, 16)
		copy(finalKey, key)
	case len(key) <= 24:
		// Pad to 24 bytes for AES-192
		finalKey = make([]byte, 24)
		copy(finalKey, key)
	default:
		// Use exactly 32 bytes for AES-256 (or truncate if longer)
		finalKey = make([]byte, 32)
		copy(finalKey, key)
	}

	key = finalKey

	// Create AES cipher
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("failed to create AES cipher: %w", err)
	}

	// Check minimum length (must be at least one block)
	if len(encryptedData) < aes.BlockSize {
		return nil, errors.New("encrypted data too short")
	}

	// Check if data length is multiple of block size (required for CBC)
	if len(encryptedData)%aes.BlockSize != 0 {
		return nil, errors.New("encrypted data length is not a multiple of block size")
	}

	// PostgreSQL's encrypt() uses all-zero IV by default
	iv := make([]byte, aes.BlockSize)

	// Create CBC decrypter
	mode := cipher.NewCBCDecrypter(block, iv)

	// Decrypt in-place
	decrypted := make([]byte, len(encryptedData))
	mode.CryptBlocks(decrypted, encryptedData)

	// Remove PKCS#7 padding
	return removePKCS7Padding(decrypted)
}

func removePKCS7Padding(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("data is empty")
	}

	// Last byte indicates padding length
	paddingLen := int(data[len(data)-1])

	// Validate padding length
	if paddingLen > len(data) || paddingLen == 0 {
		return nil, errors.New("invalid padding length")
	}

	// Check that all padding bytes have the same value
	for i := len(data) - paddingLen; i < len(data); i++ {
		if data[i] != byte(paddingLen) {
			return nil, errors.New("invalid padding")
		}
	}

	// Return data without padding
	result := data[:len(data)-paddingLen]

	return result, nil
}

func createHMAC(data []byte, secret string) []byte {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write(data)

	return h.Sum(nil)
}

// GetUserID extracts user ID from Gin context
func GetUserID(c *gin.Context) (string, bool) {
	userID, exists := c.Get("user_id")
	if !exists {
		return "", false
	}

	userIDStr, ok := userID.(string)

	return userIDStr, ok
}
