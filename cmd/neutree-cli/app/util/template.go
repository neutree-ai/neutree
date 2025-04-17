package util

import (
	"bytes"
	"html/template"
	"strings"

	"github.com/pkg/errors"
)

// ParseTemplate validates and parses passed as argument template
func ParseTemplate(strtmpl string, obj interface{}) ([]byte, error) {
	var buf bytes.Buffer

	tmpl, err := template.New("template").Parse(strtmpl)
	if err != nil {
		return nil, errors.Wrap(err, "error when parsing template")
	}

	err = tmpl.Execute(&buf, obj)
	if err != nil {
		return nil, errors.Wrap(err, "error when executing template")
	}

	return []byte(removeEmptyLines(buf.String())), nil
}

func removeEmptyLines(str string) string {
	lines := strings.Split(str, "\n")
	var nonEmptyLines []string

	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			nonEmptyLines = append(nonEmptyLines, line)
		}
	}

	return strings.Join(nonEmptyLines, "\n")
}
