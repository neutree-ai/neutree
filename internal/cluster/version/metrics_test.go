package version

import (
	"errors"
	"testing"

	mastermindssemver "github.com/Masterminds/semver/v3"
)

func TestMetricsClusterVersionGates(test *testing.T) {
	cases := []struct {
		name string
		gate string
	}{
		{"kube state metrics", MinKubeStateMetricsClusterVersion},
		{"managed metrics exporters", MinManagedMetricsExporterClusterVersion},
	}

	for _, tc := range cases {
		test.Run(tc.name, func(test *testing.T) {
			if tc.gate != "v1.1.0" {
				test.Errorf("gate = %q, want v1.1.0", tc.gate)
			}
		})
	}
}

func TestSupportsKubeStateMetrics(test *testing.T) {
	testMetricsClusterVersionSupport(test, SupportsKubeStateMetrics)
}

func TestSupportsManagedMetricsExporters(test *testing.T) {
	testMetricsClusterVersionSupport(test, SupportsManagedMetricsExporters)
}

func testMetricsClusterVersionSupport(test *testing.T, supports func(string) (bool, error)) {
	test.Helper()
	cases := []struct {
		name    string
		version string
		want    bool
		wantErr string
	}{
		{"older release", "v1.0.9", false, ""},
		{"older RC", "v1.0.9-rc.1", false, ""},
		{"older nightly", "v1.0.9-nightly.20261021", false, ""},
		{"older build metadata", "v1.0.9+build.1", false, ""},
		{"minimum release", "v1.1.0", true, ""},
		{"minimum without prefix", "1.1.0", true, ""},
		{"minimum RC", "v1.1.0-rc.1", true, ""},
		{"minimum RC without prefix", "1.1.0-rc.1", true, ""},
		{"minimum nightly", "v1.1.0-nightly.20261021", true, ""},
		{"minimum nightly without prefix", "1.1.0-nightly.20261021", true, ""},
		{"minimum build metadata", "v1.1.0+build.1", true, ""},
		{"minimum RC with build metadata", "v1.1.0-rc.1+build.1", true, ""},
		{"short version", "v1.1", true, ""},
		{"newer release", "v1.2.0", true, ""},
		{"newer RC", "v1.2.0-rc.1", true, ""},
		{"padded release", " v1.1.0 ", true, ""},
		{"padded RC", "\tv1.1.0-rc.1\n", true, ""},
		{"empty", "", false, ""},
		{"whitespace", " \t\n", false, ""},
		{"invalid", "invalid", false, `failed to parse version "invalid": Invalid Semantic Version`},
		{"padded invalid", " invalid ", false, `failed to parse version "invalid": Invalid Semantic Version`},
		{"unversioned nightly", "nightly", false, `failed to parse version "nightly": Invalid Semantic Version`},
	}
	for _, tc := range cases {
		test.Run(tc.name, func(test *testing.T) {
			got, err := supports(tc.version)
			if got != tc.want {
				test.Errorf("support(%q) = %t, want %t", tc.version, got, tc.want)
			}
			if tc.wantErr == "" {
				if err != nil {
					test.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || err.Error() != tc.wantErr || !errors.Is(err, mastermindssemver.ErrInvalidSemVer) {
				test.Errorf("error = %v, want wrapped %q", err, tc.wantErr)
			}
		})
	}
}

func TestSupportsClusterVersionAtLeast(test *testing.T) {
	cases := []struct {
		name    string
		version string
		minimum string
		want    bool
		wantErr string
	}{
		{"independent minimum", "v1.1.0", "v1.2.0", false, ""},
		{"base at minimum", "v1.1.0-rc.1", "v1.1.0", true, ""},
		{"invalid minimum", "v1.1.0", "invalid", false, "Invalid Semantic Version"},
		{"empty minimum", "v1.1.0", "", false, "Invalid Semantic Version"},
		{"empty version skips minimum", "", "invalid", false, ""},
		{"whitespace version skips minimum", " \t\n", "invalid", false, ""},
		{"invalid version checked first", "invalid", "invalid", false, `failed to parse version "invalid": Invalid Semantic Version`},
	}

	for _, tc := range cases {
		test.Run(tc.name, func(test *testing.T) {
			got, err := supportsClusterVersionAtLeast(tc.version, tc.minimum)
			if got != tc.want {
				test.Errorf("supportsClusterVersionAtLeast(%q, %q) = %t, want %t", tc.version, tc.minimum, got, tc.want)
			}
			if tc.wantErr == "" {
				if err != nil {
					test.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || err.Error() != tc.wantErr || !errors.Is(err, mastermindssemver.ErrInvalidSemVer) {
				test.Errorf("error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}
