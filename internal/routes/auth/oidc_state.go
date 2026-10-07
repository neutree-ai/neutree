package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"golang.org/x/crypto/hkdf"
)

const (
	// oidcStateCookie carries an OIDC login across the round trip through the
	// provider. It is scoped to the callback path and cleared there. There is
	// one per browser, so of two concurrent logins only the later one succeeds.
	oidcStateCookie = "neutree_oidc_state"
	// oidcStateTTL is how long a user may take at the provider to log in.
	oidcStateTTL = 10 * time.Minute

	// oidcStateKeyInfo separates the state cookie key from every other use of
	// the secret it is derived from. Changing it invalidates in-flight logins.
	oidcStateKeyInfo = "neutree oidc login state cookie v1"
)

var errInvalidLoginState = errors.New("invalid OIDC login state")

// oidcLoginState is what the callback needs from the authorize request. It is
// kept by the browser, encrypted, so any neutree-api replica can finish the
// login and nothing is stored server side.
type oidcLoginState struct {
	State      string `json:"s"`
	Nonce      string `json:"n"`
	Verifier   string `json:"v"`
	RedirectTo string `json:"r"`
	ExpiresAt  int64  `json:"e"`
}

// stateCodec encrypts and authenticates login states with AES-256-GCM. A
// sealed state is "<source name>.<ciphertext>": the callback learns from it
// which identity source to finish the login with, and the name is bound into
// the ciphertext as additional data, so a state sealed for one source does not
// open for another.
type stateCodec struct {
	aead cipher.AEAD
}

// newStateCodec derives the cookie key from secret with HKDF-SHA256. secret is
// the JWT secret neutree-api already shares with GoTrue and PostgREST: it is
// the same on every replica and needs no extra provisioning, and HKDF keeps
// the derived key independent of the secret's other uses.
func newStateCodec(secret string) (*stateCodec, error) {
	if secret == "" {
		return nil, errors.New("state cookie secret is empty")
	}

	key := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, []byte(secret), nil, []byte(oidcStateKeyInfo)), key); err != nil {
		return nil, fmt.Errorf("derive state cookie key: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	return &stateCodec{aead: aead}, nil
}

// stateAAD binds a sealed state to the cookie and the identity source it was made for.
func stateAAD(source string) []byte {
	return []byte(oidcStateCookie + "|" + source)
}

func (s *stateCodec) seal(source string, state *oidcLoginState) (string, error) {
	plaintext, err := json.Marshal(state)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}

	return source + "." + base64.RawURLEncoding.EncodeToString(s.aead.Seal(nonce, nonce, plaintext, stateAAD(source))), nil
}

// open decrypts a sealed state, checks that it has not expired, and returns
// it with the identity source it was sealed for.
func (s *stateCodec) open(value string, now time.Time) (string, *oidcLoginState, error) {
	source, sealed, found := strings.Cut(value, ".")
	if !found || source == "" {
		return "", nil, fmt.Errorf("%w: malformed", errInvalidLoginState)
	}

	state, err := s.openFor(source, sealed, now)
	if err != nil {
		return "", nil, err
	}

	return source, state, nil
}

func (s *stateCodec) openFor(source, value string, now time.Time) (*oidcLoginState, error) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) < s.aead.NonceSize() {
		return nil, fmt.Errorf("%w: malformed", errInvalidLoginState)
	}

	plaintext, err := s.aead.Open(nil, raw[:s.aead.NonceSize()], raw[s.aead.NonceSize():], stateAAD(source))
	if err != nil {
		return nil, fmt.Errorf("%w: does not authenticate", errInvalidLoginState)
	}

	var state oidcLoginState
	if err := json.Unmarshal(plaintext, &state); err != nil {
		return nil, fmt.Errorf("%w: %w", errInvalidLoginState, err)
	}

	if now.Unix() > state.ExpiresAt {
		return nil, fmt.Errorf("%w: expired", errInvalidLoginState)
	}

	return &state, nil
}
