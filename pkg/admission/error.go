package admission

import "fmt"

// Error is an expected, client-facing admission rejection.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

// Error implements the standard error interface.
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Hint == "" {
		return fmt.Sprintf("admission rejection %d: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("admission rejection %d: %s (hint: %s)", e.Code, e.Message, e.Hint)
}
