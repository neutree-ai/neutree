package component

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHAMiVersionConstants(test *testing.T) {
	cases := []struct {
		name     string
		actual   string
		expected string
	}{
		{name: "HAMi", actual: HAMi, expected: "v2.10.0"},
		{name: "legacy HAMi", actual: LegacyHAMi, expected: "v2.9.0"},
	}

	for _, testCase := range cases {
		test.Run(testCase.name, func(test *testing.T) {
			assert.Equal(test, testCase.expected, testCase.actual)
		})
	}
}
