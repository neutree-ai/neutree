package util

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseURLComponents(t *testing.T) {
	tests := []struct {
		name    string
		rawURL  string
		want    *URLComponents
		wantErr bool
	}{
		{
			name:   "https default port",
			rawURL: "https://api.openai.com",
			want:   &URLComponents{Scheme: "https", Host: "api.openai.com", Port: 443, Path: "/"},
		},
		{
			name:   "http default port",
			rawURL: "http://localhost",
			want:   &URLComponents{Scheme: "http", Host: "localhost", Port: 80, Path: "/"},
		},
		{
			name:   "explicit port",
			rawURL: "https://example.com:8443/v1",
			want:   &URLComponents{Scheme: "https", Host: "example.com", Port: 8443, Path: "/v1"},
		},
		{
			name:   "url with path",
			rawURL: "https://api.example.com/v1/chat",
			want:   &URLComponents{Scheme: "https", Host: "api.example.com", Port: 443, Path: "/v1/chat"},
		},
		{
			name:   "url without path defaults to /",
			rawURL: "https://api.example.com",
			want:   &URLComponents{Scheme: "https", Host: "api.example.com", Port: 443, Path: "/"},
		},
		{
			name:    "missing scheme",
			rawURL:  "api.openai.com",
			wantErr: true,
		},
		{
			name:    "unsupported scheme",
			rawURL:  "ftp://example.com",
			wantErr: true,
		},
		{
			name:    "empty host",
			rawURL:  "https://",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseURLComponents(tt.rawURL)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
