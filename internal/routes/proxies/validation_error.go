package proxies

import (
	"errors"
	"net/http"
)

type validationError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Hint       string `json:"hint"`
	HTTPStatus int    `json:"-"`
}

type codedValidationError interface {
	error
	ErrorCode() string
}

type hintedValidationError interface {
	error
	ErrorHint() string
}

func providerValidationError(err error, defaultCode string) *validationError {
	code := defaultCode

	var coded codedValidationError
	if errors.As(err, &coded) && coded.ErrorCode() != "" {
		code = coded.ErrorCode()
	}

	hint := ""

	var hinted hintedValidationError
	if errors.As(err, &hinted) {
		hint = hinted.ErrorHint()
	}

	return &validationError{
		Code:    code,
		Message: err.Error(),
		Hint:    hint,
	}
}

// internalServerValidationError is the response for validation-time
// infrastructure failures (e.g. a database outage). The underlying error is
// deliberately not included: it reveals internal state and the caller cannot
// act on it either way.
func internalServerValidationError() *validationError {
	return &validationError{
		Code:       "500",
		Message:    "internal server error",
		HTTPStatus: http.StatusInternalServerError,
	}
}
