package util

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestGetImagePrefix(t *testing.T) {
	tests := []struct {
		name          string
		imageRegistry *v1.ImageRegistry
		want          string
		wantErr       bool
	}{
		{
			name: "valid URL with standard repository",
			imageRegistry: &v1.ImageRegistry{
				Spec: &v1.ImageRegistrySpec{
					URL:        "https://registry.example.com",
					Repository: "my-repo",
				},
			},
			want:    "registry.example.com/my-repo",
			wantErr: false,
		},
		{
			name: "invalid URL format",
			imageRegistry: &v1.ImageRegistry{
				Spec: &v1.ImageRegistrySpec{
					URL:        "::invalid-url::",
					Repository: "repo",
				},
			},
			want:    "",
			wantErr: true,
		},
		{
			name: "URL with port number",
			imageRegistry: &v1.ImageRegistry{
				Spec: &v1.ImageRegistrySpec{
					URL:        "https://registry.example.com:5000",
					Repository: "prod",
				},
			},
			want:    "registry.example.com:5000/prod",
			wantErr: false,
		},
		{
			name: "URL with port number and no repository",
			imageRegistry: &v1.ImageRegistry{
				Spec: &v1.ImageRegistrySpec{
					URL:        "https://registry.example.com:5000",
					Repository: "",
				},
			},
			want:    "registry.example.com:5000",
			wantErr: false,
		},
		{
			name: "invalid URL with empty host",
			imageRegistry: &v1.ImageRegistry{
				Spec: &v1.ImageRegistrySpec{
					URL:        "https://",
					Repository: "repo",
				},
			},
			want:    "",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := GetImagePrefix(tt.imageRegistry)
			if (err != nil) != tt.wantErr {
				t.Errorf("getImagePrefix() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("getImagePrefix() = %v, want %v", got, tt.want)
			}
		})
	}
}
