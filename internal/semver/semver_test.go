package semver

import (
	"testing"
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
