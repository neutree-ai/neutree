package proxies

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

type fakeStructuredError struct {
	code    string
	message string
	hint    string
}

func (e fakeStructuredError) Error() string     { return e.message }
func (e fakeStructuredError) ErrorCode() string { return e.code }
func (e fakeStructuredError) ErrorHint() string { return e.hint }

func TestProviderValidationError(t *testing.T) {
	assert.Equal(t, &validationError{
		Code:    "INVALID_CACHE_CONFIGURATION",
		Message: "cache not ready",
	}, providerValidationError(errors.New("cache not ready"), "INVALID_CACHE_CONFIGURATION"))

	wrapped := fmt.Errorf("cache check: %w", fakeStructuredError{
		code:    "ENDPOINT_CACHE_CUSTOM_OVERRIDE",
		message: "remove custom LMCACHE_CONFIG_FILE",
		hint:    "LMCACHE_CONFIG_FILE",
	})
	assert.Equal(t, &validationError{
		Code:    "ENDPOINT_CACHE_CUSTOM_OVERRIDE",
		Message: "cache check: remove custom LMCACHE_CONFIG_FILE",
		Hint:    "LMCACHE_CONFIG_FILE",
	}, providerValidationError(wrapped, "INVALID_CACHE_CONFIGURATION"))

	hintOnly := fakeStructuredError{message: "invalid node", hint: "worker..a"}
	assert.Equal(t, &validationError{
		Code:    "10208",
		Message: "invalid node",
		Hint:    "worker..a",
	}, providerValidationError(hintOnly, "10208"))
}
