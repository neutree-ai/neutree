package v1

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateModelName(t *testing.T) {
	validNames := []string{
		"a",
		"model-1",
		"e2e-model_test.v2",
		strings.Repeat("a", 63),
	}

	for _, name := range validNames {
		t.Run("valid_"+name, func(t *testing.T) {
			assert.NoError(t, ValidateModelName(name))
		})
	}

	invalidNames := []string{
		"",
		" Model",
		"model ",
		"Invalid_Name",
		"bad/name",
		`bad\name`,
		".",
		"..",
		"-bad",
		"bad-",
		"_bad",
		"bad_",
		"bad name",
		"bad:name",
		strings.Repeat("a", 64),
	}

	for _, name := range invalidNames {
		t.Run("invalid_"+name, func(t *testing.T) {
			err := ValidateModelName(name)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "model name")
		})
	}
}
