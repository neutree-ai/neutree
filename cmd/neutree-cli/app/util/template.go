package util

import (
	"bytes"
	"html/template"
	"os"
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

// BatchParseTemplateFiles batch parses template files and overwrite orignal template file.
// it only for params the same case.
func BatchParseTemplateFiles(templateFiles []string, obj interface{}) error {
	for _, templateFile := range templateFiles {
		templateContent, err := os.ReadFile(templateFile)
		if err != nil {
			return errors.Wrapf(err, "read template file failed, file path: %s", templateFile)
		}

		parsedContent, err := ParseTemplate(string(templateContent), obj)
		if err != nil {
			return errors.Wrapf(err, "parse template file failed, file path: %s", templateFile)
		}

		err = os.WriteFile(templateFile, parsedContent, 0600)
		if err != nil {
			return errors.Wrapf(err, "write template file failed, file path: %s", templateFile)
		}
	}

	return nil
}
