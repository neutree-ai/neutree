package util

import "testing"

func TestIsHTTPOrHTTPSURL(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"http://example.com", true},
		{"https://example.com", true},
		{"https://example.com:8428", true},
		{"http://127.0.0.1:8428", true},
		{"https://[::1]:8428", true},
		{"ftp://example.com", false},
		{"example.com", false},
		{"", false},
		{"   https://example.com   ", true},
		{"http:/example.com", false},
		{"http://:8428", false},
	}

	for _, tt := range tests {
		got := IsHTTPOrHTTPSURL(tt.input)
		if got != tt.want {
			t.Errorf("IsHTTPOrHTTPSURL(%q) = %v; want %v", tt.input, got, tt.want)
		}
	}
}
