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

func Test_GetImageRegistryAuthInfo(t *testing.T) {
	tests := []struct {
		name          string
		imageRegistry *v1.ImageRegistry
		wantUser      string
		wantPassword  string
		wantErr       bool
	}{
		{
			name: "Username and Password provided",
			imageRegistry: &v1.ImageRegistry{
				Spec: &v1.ImageRegistrySpec{
					AuthConfig: v1.ImageRegistryAuthConfig{
						Username: "testuser",
						Password: "testpassword",
					},
				},
			},
			wantUser:     "testuser",
			wantPassword: "testpassword",
			wantErr:      false,
		},
		{
			name: "only Username provided",
			imageRegistry: &v1.ImageRegistry{
				Spec: &v1.ImageRegistrySpec{
					AuthConfig: v1.ImageRegistryAuthConfig{
						Username: "testuser",
					},
				},
			},
			wantUser:     "",
			wantPassword: "",
			wantErr:      false,
		},
		{
			name: "Auth provided in base64",
			imageRegistry: &v1.ImageRegistry{
				Spec: &v1.ImageRegistrySpec{
					AuthConfig: v1.ImageRegistryAuthConfig{
						Auth: "dGVzdHVzZXI6dGVzdHBhc3N3b3Jk", // base64 for "testuser:testpassword"
					},
				},
			},
			wantUser:     "testuser",
			wantPassword: "testpassword",
			wantErr:      false,
		},
		{
			name: "Auth provided password with special characters in base64",
			imageRegistry: &v1.ImageRegistry{
				Spec: &v1.ImageRegistrySpec{
					AuthConfig: v1.ImageRegistryAuthConfig{
						Auth: "dGVzdHVzZXI6cGFzczp3b3Jk", // base64 for "testuser:pass:word"
					},
				},
			},
			wantUser:     "testuser",
			wantPassword: "pass:word",
			wantErr:      false,
		},
		{
			name: "Auth provided password is empty in base64",
			imageRegistry: &v1.ImageRegistry{
				Spec: &v1.ImageRegistrySpec{
					AuthConfig: v1.ImageRegistryAuthConfig{
						Auth: "dGVzdHVzZXI6", // base64 for "testuser:"
					},
				},
			},
			wantUser:     "",
			wantPassword: "",
			wantErr:      false,
		},
		{
			name: "Auth provided user is empty in base64",
			imageRegistry: &v1.ImageRegistry{
				Spec: &v1.ImageRegistrySpec{
					AuthConfig: v1.ImageRegistryAuthConfig{
						Auth: "OnRlc3RwYXNzd29yZA==", // base64 for ":testpassword"
					},
				},
			},
			wantUser:     "",
			wantPassword: "",
			wantErr:      false,
		},
		{
			name: "username and password privileged",
			imageRegistry: &v1.ImageRegistry{
				Spec: &v1.ImageRegistrySpec{
					AuthConfig: v1.ImageRegistryAuthConfig{
						Username: "admin",
						Password: "adminpass",
						Auth:     "dGVzdHVzZXI6dGVzdHBhc3N3b3Jk", // base64 for "testuser:testpassword"
					},
				},
			},
			wantUser:     "admin",
			wantPassword: "adminpass",
			wantErr:      false,
		},
		{
			name: "Empty AuthConfig",
			imageRegistry: &v1.ImageRegistry{
				Spec: &v1.ImageRegistrySpec{
					AuthConfig: v1.ImageRegistryAuthConfig{},
				},
			},
			wantUser:     "",
			wantPassword: "",
			wantErr:      false,
		},
		{
			name: "Invalid base64 Auth format",
			imageRegistry: &v1.ImageRegistry{
				Spec: &v1.ImageRegistrySpec{
					AuthConfig: v1.ImageRegistryAuthConfig{
						Auth: "dGVzdHVzZXItdGVzdHBhc3N3b3Jk", // invalid base64 for "testuser-testpassword"
					},
				},
			},
			wantUser:     "",
			wantPassword: "",
			wantErr:      true,
		},
		{
			name: "Invalid base64 Auth",
			imageRegistry: &v1.ImageRegistry{
				Spec: &v1.ImageRegistrySpec{
					AuthConfig: v1.ImageRegistryAuthConfig{
						Auth: "invalid-base64",
					},
				},
			},
			wantUser:     "",
			wantPassword: "",
			wantErr:      true,
		},
		{
			name:          "Nil ImageRegistry",
			imageRegistry: nil,
			wantUser:      "",
			wantPassword:  "",
			wantErr:       true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotUser, gotPassword, err := GetImageRegistryAuthInfo(tt.imageRegistry)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetImageRegistryAuthInfo() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if gotUser != tt.wantUser {
				t.Errorf("GetImageRegistryAuthInfo() gotUser = %v, want %v", gotUser, tt.wantUser)
			}
			if gotPassword != tt.wantPassword {
				t.Errorf("GetImageRegistryAuthInfo() gotPassword = %v, want %v", gotPassword, tt.wantPassword)
			}
		})
	}
}
