package v1

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

const maxResourceNameLength = 63

// resourceNameRegex is the character set a resource name may use. It is the
// intersection of what stays intact in a URL path, a Kong route and an ACL
// group, a Kubernetes identity, and a filesystem path -- every place a
// metadata.name is pasted into verbatim.
var resourceNameRegex = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]{0,61}[a-z0-9])?$`)

// ValidateResourceName enforces the naming contract for a metadata.name that
// becomes part of a resource's identity rather than its presentation. A
// human-readable name belongs in metadata.display_name, which carries no such
// restriction.
//
// kind names the resource in the error message ("model", "external endpoint")
// and is expected to be lowercase, so the message reads as a sentence.
func ValidateResourceName(kind, name string) error {
	if name != strings.TrimSpace(name) {
		return fmt.Errorf("%s name must not contain leading or trailing whitespace", kind)
	}

	if len(name) == 0 {
		return fmt.Errorf("%s name is required", kind)
	}

	// Counted in runes, not bytes: a name of non-ASCII runes is rejected either
	// way, but it is the character set that is wrong with it, and a byte count
	// would report a length it does not have. Names that pass are ASCII, where
	// the two counts agree.
	if utf8.RuneCountInString(name) > maxResourceNameLength {
		return fmt.Errorf("%s name must be at most %d characters", kind, maxResourceNameLength)
	}

	if strings.ToLower(name) != name {
		return fmt.Errorf("%s name must be lowercase", kind)
	}

	if !resourceNameRegex.MatchString(name) {
		return fmt.Errorf("%s name must consist of lowercase alphanumeric characters, '_', '-', or '.', "+
			"and must start and end with an alphanumeric character", kind)
	}

	return nil
}
