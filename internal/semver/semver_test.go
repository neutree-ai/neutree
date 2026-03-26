package semver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLessThan(t *testing.T) {
	tests := []struct {
		name     string
		versionA string
		versionB string
		want     bool
		wantErr  bool
	}{
		{
			name:     "A less than B",
			versionA: "1.0.0",
			versionB: "2.0.0",
			want:     true,
			wantErr:  false,
		},
		{
			name:     "A equal to B",
			versionA: "1.0.0",
			versionB: "1.0.0",
			want:     false,
			wantErr:  false,
		},
		{
			name:     "A greater than B",
			versionA: "2.0.0",
			versionB: "1.0.0",
			want:     false,
			wantErr:  false,
		},
		{
			name:     "Invalid version A",
			versionA: "invalid",
			versionB: "1.0.0",
			want:     false,
			wantErr:  true,
		},
		{
			name:     "Invalid version B",
			versionA: "1.0.0",
			versionB: "invalid",
			want:     false,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := LessThan(tt.versionA, tt.versionB)
			if (err != nil) != tt.wantErr {
				t.Errorf("LessThan() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("LessThan() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBaseVersion(t *testing.T) {
	tests := []struct {
		name     string
		version  string
		expected string
		wantErr  bool
	}{
		{
			name:     "plain version with v prefix",
			version:  "v0.8.5",
			expected: "v0.8.5",
		},
		{
			name:     "plain version without v prefix",
			version:  "0.8.5",
			expected: "0.8.5",
		},
		{
			name:     "cuda variant",
			version:  "v0.17.1-cu130",
			expected: "v0.17.1",
		},
		{
			name:     "cuda variant without v prefix",
			version:  "0.17.1-cu130",
			expected: "0.17.1",
		},
		{
			name:     "rocm variant",
			version:  "v0.17.1-rocm60",
			expected: "v0.17.1",
		},
		{
			name:     "build metadata only",
			version:  "v1.2.3+build456",
			expected: "v1.2.3",
		},
		{
			name:     "prerelease and build metadata",
			version:  "v1.2.3-beta.1+build",
			expected: "v1.2.3",
		},
		{
			name:     "existing vllm version",
			version:  "v0.11.2",
			expected: "v0.11.2",
		},
		{
			name:     "empty version",
			version:  "",
			expected: "",
		},
		{
			name:    "invalid version",
			version: "invalid",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := BaseVersion(tt.version)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}
