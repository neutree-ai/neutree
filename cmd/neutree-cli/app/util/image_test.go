package util

import (
	"testing"
)

func TestReplaceImageRegistry(t *testing.T) {
	tests := []struct {
		name           string
		imageURL       string
		mirrorRegistry string
		want           string
		wantErr        bool
	}{
		{
			name:           "tag based image",
			imageURL:       "docker.io/library/nginx:latest",
			mirrorRegistry: "mirror.example.com",
			want:           "mirror.example.com/library/nginx:latest",
			wantErr:        false,
		},
		{
			name:           "based image no registry",
			imageURL:       "nginx:latest",
			mirrorRegistry: "mirror.example.com",
			want:           "mirror.example.com/library/nginx:latest",
			wantErr:        false,
		},
		{
			name:           "digest based image",
			imageURL:       "docker.io/library/nginx@sha256:11522ec7a80b3b775b35a6544fa092def2f6bc1c5986f57ea2a6e712a8ad2647",
			mirrorRegistry: "mirror.example.com",
			want:           "mirror.example.com/library/nginx@sha256:11522ec7a80b3b775b35a6544fa092def2f6bc1c5986f57ea2a6e712a8ad2647",
			wantErr:        false,
		},
		{
			name:           "invalid image url",
			imageURL:       "invalid url",
			mirrorRegistry: "mirror.example.com",
			want:           "",
			wantErr:        true,
		},
		{
			name:           "empty mirror registry",
			imageURL:       "docker.io/library/nginx:latest",
			mirrorRegistry: "",
			want:           "docker.io/library/nginx:latest",
			wantErr:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ReplaceImageRegistry(tt.imageURL, tt.mirrorRegistry)
			if (err != nil) != tt.wantErr {
				t.Errorf("ReplaceImageRegistry() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("ReplaceImageRegistry() = %v, want %v", got, tt.want)
			}
		})
	}
}
