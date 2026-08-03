package v1

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeModelAlias(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"already normalized", "qwen3", "qwen3"},
		{"uppercase", "Qwen3", "qwen3"},
		{"surrounding whitespace", " Qwen3 ", "qwen3"},
		{"tab and newline are whitespace too", "\tQwen3\n", "qwen3"},
		{"inner spacing is preserved", "Qwen 3 Chat", "qwen 3 chat"},
		{"NFKC folds fullwidth forms", "\uff31\uff57\uff45\uff4e\uff13", "qwen3"},
		{"NFKC folds a no-break space into trimmable whitespace", "\u00a0Qwen3\u00a0", "qwen3"},
		{"NFKC composes decomposed accents", "Cafe\u0301", "caf\u00e9"},
		{"empty stays empty", "   ", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, NormalizeModelAlias(tt.input))
		})
	}
}

// The whole point of the normalized column is that these three spellings
// collide, so the unique index rejects the second one. db/dbtest asserts the
// database half; this asserts the Go half that feeds it.
func TestNormalizeModelAliasCollides(t *testing.T) {
	normalized := NormalizeModelAlias("Qwen3")

	for _, variant := range []string{"qwen3", " Qwen3 ", "QWEN3", "Ｑｗｅｎ３"} {
		assert.Equal(t, normalized, NormalizeModelAlias(variant), "variant %q should collide with %q", variant, "Qwen3")
	}

	assert.NotEqual(t, normalized, NormalizeModelAlias("Qwen 3"), "inner spacing is significant")
}

func TestValidateModelAlias(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr string
	}{
		{name: "plain", input: "Qwen3"},
		{name: "inner spaces", input: "Qwen 3 Chat"},
		{name: "surrounding whitespace is tolerated", input: "  Qwen3  "},
		{name: "mixed script", input: "通义千问 3"},
		{name: "at the length limit", input: strings.Repeat("a", maxModelAliasLength)},
		{name: "empty", input: "", wantErr: "empty"},
		{name: "whitespace only", input: "   ", wantErr: "empty"},
		{name: "over the length limit", input: strings.Repeat("a", maxModelAliasLength+1), wantErr: "at most"},
		{name: "control character", input: "Qwen\x003", wantErr: "control characters"},
		{name: "newline", input: "Qwen\n3", wantErr: "control characters"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateModelAlias(tt.input)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// The length bound is checked on the trimmed form while the constant is
// described in terms of the normalized one. That is only sound because
// lowercasing cannot change the rune count: strings.ToLower applies
// unicode.ToLower rune by rune (simple case mapping), so unlike full Unicode
// lowercasing it never expands U+0130 into "i" + combining dot. Pinned here so
// the two forms cannot silently drift apart.
func TestModelAliasLengthBoundIsTheSameOnEitherForm(t *testing.T) {
	inputs := []string{
		"Qwen3",
		"\u0130stanbul", // U+0130 capital I with dot above
		"\u01c5",        // U+01C5 titlecase digraph
		"\u1e68",        // U+1E68 S with dot above and below
		"\u1e9estra\u00dfe", // U+1E9E capital sharp s
		strings.Repeat("\u0130", maxModelAliasLength),
		strings.Repeat("\u0130", maxModelAliasLength+1),
	}

	for _, in := range inputs {
		trimmed := trimModelAlias(in)
		normalized := NormalizeModelAlias(in)

		assert.Equal(t, utf8.RuneCountInString(trimmed), utf8.RuneCountInString(normalized),
			"lowercasing must not change the rune count of %q", in)

		overTrimmed := utf8.RuneCountInString(trimmed) > maxModelAliasLength
		overNormalized := utf8.RuneCountInString(normalized) > maxModelAliasLength
		assert.Equal(t, overTrimmed, overNormalized,
			"the bound must reject %q on both forms or on neither", in)
	}
}

// An alias is a display name, so it must not inherit the physical-name rules:
// ValidateModelName forces lowercase and caps at 63 characters.
func TestValidateModelAliasIsNotValidateModelName(t *testing.T) {
	assert.NoError(t, ValidateModelAlias("Qwen3 Chat"))
	assert.Error(t, ValidateModelName("Qwen3 Chat"))

	long := strings.Repeat("a", 100)
	assert.NoError(t, ValidateModelAlias(long))
	assert.Error(t, ValidateModelName(long))
}
