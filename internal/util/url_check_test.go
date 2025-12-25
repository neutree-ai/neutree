package util

import "testing"

func TestIsHTTPOrHTTPSURL(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"http://example.com", true},
		{"https://example.com", true},
		{"ftp://example.com", false},
		{"example.com", false},
		{"", false},
		{"   https://example.com   ", true},
		{"http:/example.com", false},
	}

	for _, tt := range tests {
		got := IsHTTPOrHTTPSURL(tt.input)
		if got != tt.want {
			t.Errorf("IsHTTPOrHTTPSURL(%q) = %v; want %v", tt.input, got, tt.want)
		}
	}
}
