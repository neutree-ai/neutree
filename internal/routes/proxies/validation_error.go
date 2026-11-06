package proxies

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type validationError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Hint       string `json:"hint"`
	HTTPStatus int    `json:"-"`
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

func rejectValidation(c *gin.Context, validationErr *validationError) {
	c.JSON(validationErrStatus(validationErr), validationErr)
	c.Abort()
}
