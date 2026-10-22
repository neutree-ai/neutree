package version

import (
	"testing"

	mastermindssemver "github.com/Masterminds/semver/v3"
)

func TestSupportsEngineIsolation(test *testing.T) {
	cases := []struct {
		name    string
		version string
		want    bool
		wantErr bool
	}{
		{"older release", "v0.9.9", false, false},
		{"gate release", "v1.0.0", false, false},
		{"gate without prefix", "1.0.0", false, false},
		{"gate RC", "v1.0.0-rc.1", false, false},
		{"gate nightly", "v1.0.0-nightly.20261021", false, false},
		{"gate build metadata", "v1.0.0+build.1", false, false},
		{"newer release", "v1.0.1", true, false},
		{"newer without prefix", "1.0.1", true, false},
		{"newer RC", "v1.0.1-rc.1", true, false},
		{"newer nightly", "v1.0.1-nightly.20261021", true, false},
		{"newer build metadata", "v1.0.1+build.1", true, false},
		{"short version", "v1.1", true, false},
		{"empty", "", false, true},
		{"whitespace", " \t\n", false, true},
		{"padded release", " v1.0.1 ", false, true},
		{"invalid", "invalid", false, true},
	}

	for _, tc := range cases {
		test.Run(tc.name, func(test *testing.T) {
			got, err := SupportsEngineIsolation(tc.version)
			if got != tc.want {
				test.Errorf("SupportsEngineIsolation(%q) = %t, want %t", tc.version, got, tc.want)
			}
			if tc.wantErr {
				if err != mastermindssemver.ErrInvalidSemVer {
					test.Errorf("error = %v, want unwrapped %v", err, mastermindssemver.ErrInvalidSemVer)
				}
			} else if err != nil {
				test.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestSupportsRayNodeIPAddress(test *testing.T) {
	cases := []struct {
		name    string
		version string
		want    bool
		wantErr bool
	}{
		{"older release", "v1.1.9", false, false},
		{"gate release", "v1.2.0", false, false},
		{"gate without prefix", "1.2.0", false, false},
		{"gate RC", "v1.2.0-rc.1", false, false},
		{"gate nightly", "v1.2.0-nightly.20261021", false, false},
		{"gate build metadata", "v1.2.0+build.1", false, false},
		{"newer release", "v1.2.1", true, false},
		{"newer without prefix", "1.2.1", true, false},
		{"newer RC", "v1.2.1-rc.1", true, false},
		{"newer nightly", "v1.2.1-nightly.20261021", true, false},
		{"newer build metadata", "v1.2.1+build.1", true, false},
		{"short gate version", "v1.2", false, false},
		{"short newer version", "v1.3", true, false},
		{"empty", "", false, true},
		{"whitespace", " \t\n", false, true},
		{"padded release", " v1.2.1 ", false, true},
		{"invalid", "invalid", false, true},
	}

	for _, tc := range cases {
		test.Run(tc.name, func(test *testing.T) {
			got, err := SupportsRayNodeIPAddress(tc.version)
			if got != tc.want {
				test.Errorf("SupportsRayNodeIPAddress(%q) = %t, want %t", tc.version, got, tc.want)
			}
			if tc.wantErr {
				if err != mastermindssemver.ErrInvalidSemVer {
					test.Errorf("error = %v, want unwrapped %v", err, mastermindssemver.ErrInvalidSemVer)
				}
			} else if err != nil {
				test.Errorf("unexpected error: %v", err)
			}
		})
	}
}
