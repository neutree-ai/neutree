package v1

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// maxModelAliasLength bounds the alias in its NFKC-normalized, whitespace-
// trimmed form -- equivalently its lowercased form, since strings.ToLower maps
// rune to rune (simple case mapping, no special casing) and so cannot change the
// count. It is deliberately far above maxResourceNameLength: an alias is a display
// name, not a BentoML tag.
const maxModelAliasLength = 128

// ModelAlias is a user-facing display name for one model version inside a
// private model registry. A model version carries at most one alias.
//
// The registry filesystem, not this table, decides which models exist. A row
// whose (ModelName, ModelVersion) no longer resolves is an orphan: it is hidden
// from reads and it does not reserve its alias, so a later write for the same
// normalized alias overwrites it.
type ModelAlias struct {
	ID int `json:"id,omitempty"`
	// ModelRegistryID references api.model_registries(id). Keying on the row id
	// rather than the registry name means deleting and recreating a registry
	// under the same name does not inherit the old registry's aliases.
	ModelRegistryID int `json:"model_registry_id,omitempty"`
	// Workspace is the owning registry's workspace. It is read-only: a database
	// trigger derives it from ModelRegistryID on every write, so anything set
	// here is discarded. Authorization keys off the registry's workspace, never
	// off this column.
	Workspace string `json:"workspace,omitempty"`
	// ModelName and ModelVersion are the physical coordinates of the model.
	ModelName    string `json:"model_name,omitempty"`
	ModelVersion string `json:"model_version,omitempty"`
	// Alias is the user input, stored verbatim; case and inner spacing are part
	// of the display name.
	Alias string `json:"alias,omitempty"`
	// AliasNormalized is what uniqueness compares. Always set it with
	// NormalizeModelAlias; the database does not derive it.
	AliasNormalized string `json:"alias_normalized,omitempty"`
}

// NormalizeModelAlias returns the comparison form of an alias: NFKC, then outer
// whitespace trimmed, then lowercased.
//
// It is computed here rather than by a Postgres expression index because
// Postgres' lower() is not a full Unicode casefold and NFKC needs an extension.
// It also deliberately does not reuse ValidateModelName's rules, which force
// lowercase physical names limited to 63 characters.
func NormalizeModelAlias(alias string) string {
	return strings.ToLower(trimModelAlias(alias))
}

// ValidateModelAlias checks an alias as typed by the user, before it is stored.
func ValidateModelAlias(alias string) error {
	for _, r := range alias {
		if unicode.IsControl(r) {
			return fmt.Errorf("model alias must not contain control characters")
		}
	}

	trimmed := trimModelAlias(alias)
	if trimmed == "" {
		return fmt.Errorf("model alias must not be empty after trimming whitespace")
	}

	if utf8.RuneCountInString(trimmed) > maxModelAliasLength {
		return fmt.Errorf("model alias must be at most %d characters", maxModelAliasLength)
	}

	return nil
}

// trimModelAlias applies NFKC first so that compatibility whitespace (a
// no-break space, say) becomes ordinary whitespace and is then trimmed.
func trimModelAlias(alias string) string {
	return strings.TrimSpace(norm.NFKC.String(alias))
}
