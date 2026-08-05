package proxies

import (
	"strconv"

	"github.com/neutree-ai/neutree/pkg/admission"
)

type validationError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Hint       string `json:"hint"`
	HTTPStatus int    `json:"-"`
}

func toAdmissionError(err *validationError) *admission.Error {
	if err == nil {
		return nil
	}
	code, parseErr := strconv.Atoi(err.Code)
	if parseErr != nil {
		return &admission.Error{Code: admissionInternalErrorCode, Message: "internal admission error"}
	}
	return &admission.Error{Code: code, Message: err.Message, Hint: err.Hint}
}
