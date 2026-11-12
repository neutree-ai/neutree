package proxies

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

const sensitiveQueryRejectedCode = "10330"

// sensitiveQueryGuard rejects PostgREST query features on a resource with
// masked fields that response masking cannot cover: projections other than
// whole columns (aliases, JSON paths, casts, embedding) and filters or
// orderings on a masked field, which act as an oracle even when no value is
// returned.
type sensitiveQueryGuard struct {
	// sensitivePaths are the masked field paths split into lower-cased segments.
	sensitivePaths [][]string
}

var plainIdentifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// newSensitiveQueryGuard returns nil when there are no masked fields, so such
// resources keep the full PostgREST query surface.
func newSensitiveQueryGuard(excludeFields map[string]struct{}) *sensitiveQueryGuard {
	if len(excludeFields) == 0 {
		return nil
	}

	guard := &sensitiveQueryGuard{}
	for path := range excludeFields {
		guard.sensitivePaths = append(guard.sensitivePaths, strings.Split(strings.ToLower(path), "."))
	}

	return guard
}

func (g *sensitiveQueryGuard) middleware(c *gin.Context) {
	if err := g.check(c.Request.URL.RawQuery); err != nil {
		rejectValidation(c, err)
		return
	}

	c.Next()
}

func (g *sensitiveQueryGuard) check(rawQuery string) *validationError {
	if g == nil || rawQuery == "" {
		return nil
	}

	// PostgREST must see exactly the parameters checked here, so anything Go
	// cannot parse unambiguously (e.g. ";" separators) is refused.
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return sensitiveQueryError("the query string is malformed: " + err.Error())
	}

	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	for _, key := range keys {
		for _, value := range values[key] {
			if err := g.checkParam(key, value); err != nil {
				return err
			}
		}
	}

	return nil
}

func (g *sensitiveQueryGuard) checkParam(key, value string) *validationError {
	switch key {
	case "select":
		return checkSelectParam(value)
	case "order":
		return g.checkOrderParam(value)
	case "or", "and", "not.or", "not.and":
		return g.checkLogicParam(key, value)
	}

	if _, reserved := postgrestReservedParams[key]; reserved {
		return nil
	}

	segments, end, ok := parseColumnRef(key, 0)
	if !ok || end != len(key) {
		return sensitiveQueryError(fmt.Sprintf("filter %q is not a column reference", key))
	}

	if g.overlaps(segments) {
		return sensitiveQueryError(fmt.Sprintf("filter %q references a masked field", key))
	}

	return nil
}

func checkSelectParam(value string) *validationError {
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "*" || item == "count()" || plainIdentifierPattern.MatchString(item) {
			continue
		}

		return sensitiveQueryError(fmt.Sprintf(
			"select item %q is not allowed; select only *, top-level columns or count()", item))
	}

	return nil
}

func (g *sensitiveQueryGuard) checkOrderParam(value string) *validationError {
	for pos := 0; ; {
		segments, end, ok := parseColumnRef(value, pos)
		if !ok {
			return sensitiveQueryError(fmt.Sprintf("order %q is not a list of column references", value))
		}

		if g.overlaps(segments) {
			return sensitiveQueryError(fmt.Sprintf("order %q references a masked field", value))
		}

		termEnd := strings.IndexByte(value[end:], ',')
		if termEnd < 0 {
			termEnd = len(value)
		} else {
			termEnd += end
		}

		if !validOrderModifiers(value[end:termEnd]) {
			return sensitiveQueryError(fmt.Sprintf("order %q is not a list of column references", value))
		}

		if termEnd == len(value) {
			return nil
		}

		pos = termEnd + 1
	}
}

func validOrderModifiers(modifiers string) bool {
	if modifiers == "" {
		return true
	}

	if !strings.HasPrefix(modifiers, ".") {
		return false
	}

	parts := strings.Split(modifiers[1:], ".")
	if len(parts) > 2 {
		return false
	}

	for i, part := range parts {
		switch part {
		case "asc", "desc":
			if i != 0 {
				return false
			}
		case "nullsfirst", "nullslast":
			if i != len(parts)-1 {
				return false
			}
		default:
			return false
		}
	}

	return true
}

// checkLogicParam checks every column reference in an and/or tree. Rather than
// mirror PostgREST's value grammar (quoting, arrays, lists), it treats every
// position after "(" or "," as a possible element start, so a reference
// hidden inside a value is still found.
func (g *sensitiveQueryGuard) checkLogicParam(key, value string) *validationError {
	trimmed := strings.TrimSpace(value)
	if !strings.HasPrefix(trimmed, "(") || !strings.HasSuffix(trimmed, ")") {
		return sensitiveQueryError(fmt.Sprintf("%s %q is not a parenthesized condition list", key, value))
	}

	for i := 0; i < len(value); i++ {
		if value[i] != '(' && value[i] != ',' {
			continue
		}

		start := i + 1
		for strings.HasPrefix(value[skipSpace(value, start):], "not.") {
			start = skipSpace(value, start) + len("not.")
		}

		segments, end, ok := parseColumnRef(value, start)
		if !ok {
			continue
		}

		end = skipSpace(value, end)
		if end < len(value) && value[end] == '.' && g.overlaps(segments) {
			return sensitiveQueryError(fmt.Sprintf("%s %q references a masked field", key, value))
		}
	}

	return nil
}

// overlaps reports whether a column reference reads a masked field or an
// object containing one. Array indexes are dropped because masked paths do not
// record them, and names are compared case-insensitively to err on the side of
// rejecting.
func (g *sensitiveQueryGuard) overlaps(segments []string) bool {
	path := make([]string, 0, len(segments))

	for _, segment := range segments {
		segment = strings.ToLower(strings.TrimSpace(segment))
		if isArrayIndex(segment) {
			continue
		}

		path = append(path, segment)
	}

	for _, sensitive := range g.sensitivePaths {
		if hasSegmentPrefix(path, sensitive) || hasSegmentPrefix(sensitive, path) {
			return true
		}
	}

	return false
}

func hasSegmentPrefix(path, prefix []string) bool {
	if len(prefix) > len(path) {
		return false
	}

	for i := range prefix {
		if path[i] != prefix[i] {
			return false
		}
	}

	return true
}

func isArrayIndex(segment string) bool {
	segment = strings.TrimPrefix(segment, "-")
	if segment == "" {
		return false
	}

	for _, r := range segment {
		if r < '0' || r > '9' {
			return false
		}
	}

	return true
}

// parseColumnRef parses a PostgREST field reference, name(->key|->>key)*,
// starting at pos. It returns the path segments and the position after it.
func parseColumnRef(s string, pos int) ([]string, int, bool) {
	name, end, ok := parseRefName(s, pos)
	if !ok {
		return nil, pos, false
	}

	segments := []string{name}

	for {
		next := skipSpace(s, end)

		switch {
		case strings.HasPrefix(s[next:], "->>"):
			next += len("->>")
		case strings.HasPrefix(s[next:], "->"):
			next += len("->")
		default:
			return segments, end, true
		}

		name, end, ok = parseRefName(s, next)
		if !ok {
			return nil, pos, false
		}

		segments = append(segments, name)
	}
}

// parseRefName parses one quoted or unquoted name. Unquoted names follow
// PostgREST: letters, digits, "_", "$", spaces and "-" not starting "->".
func parseRefName(s string, pos int) (string, int, bool) {
	pos = skipSpace(s, pos)

	if pos < len(s) && s[pos] == '"' {
		var name strings.Builder

		for i := pos + 1; i < len(s); i++ {
			switch s[i] {
			case '\\':
				i++
				if i >= len(s) {
					return "", pos, false
				}

				name.WriteByte(s[i])
			case '"':
				return name.String(), i + 1, true
			default:
				name.WriteByte(s[i])
			}
		}

		return "", pos, false
	}

	end := pos

	for end < len(s) {
		r, size := utf8.DecodeRuneInString(s[end:])

		if r == '-' && strings.HasPrefix(s[end:], "->") {
			break
		}

		if r != '-' && r != '_' && r != '$' && r != ' ' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			break
		}

		end += size
	}

	if end == pos {
		return "", pos, false
	}

	return s[pos:end], end, true
}

func skipSpace(s string, pos int) int {
	for pos < len(s) {
		r, size := utf8.DecodeRuneInString(s[pos:])
		if !unicode.IsSpace(r) {
			break
		}

		pos += size
	}

	return pos
}

func sensitiveQueryError(hint string) *validationError {
	return &validationError{
		Code:       sensitiveQueryRejectedCode,
		Message:    "query not allowed on a resource with masked fields",
		Hint:       hint,
		HTTPStatus: http.StatusBadRequest,
	}
}
