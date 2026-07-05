package promtext

import (
	"strconv"
	"strings"
)

type Sample struct {
	Name   string
	Labels map[string]string
	Value  float64
}

func Parse(raw string) []Sample {
	var result []Sample

	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		metricPart, valuePart, ok := splitSampleLine(line)
		if !ok {
			continue
		}

		value, err := strconv.ParseFloat(valuePart, 64)
		if err != nil {
			continue
		}

		name, labels := parseMetricPart(metricPart)
		if name == "" {
			continue
		}

		result = append(result, Sample{Name: name, Labels: labels, Value: value})
	}

	return result
}

func splitSampleLine(line string) (string, string, bool) {
	inQuotes := false
	escapeNext := false

	for i := len(line) - 1; i >= 0; i-- {
		ch := line[i]
		if escapeNext {
			escapeNext = false
			continue
		}
		if ch == '\\' {
			escapeNext = true
			continue
		}
		if ch == '"' {
			inQuotes = !inQuotes
			continue
		}
		if !inQuotes && (ch == ' ' || ch == '\t') {
			metricPart := strings.TrimSpace(line[:i])
			valuePart := strings.TrimSpace(line[i+1:])
			return metricPart, valuePart, metricPart != "" && valuePart != ""
		}
	}

	return "", "", false
}

func parseMetricPart(metricPart string) (string, map[string]string) {
	labels := map[string]string{}
	open := strings.Index(metricPart, "{")
	if open < 0 {
		return metricPart, labels
	}

	close := strings.LastIndex(metricPart, "}")
	if close < open {
		return "", nil
	}

	name := metricPart[:open]
	for _, item := range splitLabelItems(metricPart[open+1 : close]) {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}

		labels[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"`)
	}

	return name, labels
}

func splitLabelItems(raw string) []string {
	var result []string
	var current strings.Builder
	inQuotes := false
	escapeNext := false

	for _, ch := range raw {
		if escapeNext {
			current.WriteRune(ch)
			escapeNext = false
			continue
		}
		if ch == '\\' {
			current.WriteRune(ch)
			escapeNext = true
			continue
		}
		if ch == '"' {
			current.WriteRune(ch)
			inQuotes = !inQuotes
			continue
		}
		if ch == ',' && !inQuotes {
			result = append(result, strings.TrimSpace(current.String()))
			current.Reset()
			continue
		}

		current.WriteRune(ch)
	}

	if current.Len() > 0 {
		result = append(result, strings.TrimSpace(current.String()))
	}

	return result
}
