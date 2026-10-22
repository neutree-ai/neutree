package version

import (
	"errors"
	"testing"

	mastermindssemver "github.com/Masterminds/semver/v3"
)

func TestClusterVersionGates(test *testing.T) {
	cases := []struct {
		name string
		gate string
	}{
		{"static node flow", StaticNodeClusterFlowVersionGate},
		{"selectable cluster", MinimumSelectableClusterVersionGate},
	}

	for _, tc := range cases {
		test.Run(tc.name, func(test *testing.T) {
			if tc.gate != "v1.0.1" {
				test.Errorf("gate = %q, want v1.0.1", tc.gate)
			}
		})
	}
}

func TestIsStaticNodeClusterFlowVersion(test *testing.T) {
	cases := []struct {
		name    string
		version string
		want    bool
		wantErr string
	}{
		{"older release", "v1.0.0", false, ""},
		{"gate release", "v1.0.1", false, ""},
		{"gate without prefix", "1.0.1", false, ""},
		{"gate RC", "v1.0.1-rc.1", false, ""},
		{"gate nightly", "v1.0.1-nightly.20261021", false, ""},
		{"gate build metadata", "v1.0.1+build.1", false, ""},
		{"newer release", "v1.0.2", true, ""},
		{"newer without prefix", "1.0.2", true, ""},
		{"newer RC", "v1.0.2-rc.1", true, ""},
		{"newer nightly", "v1.0.2-nightly.20261021", true, ""},
		{"newer build metadata", "v1.0.2+build.1", true, ""},
		{"short version", "v1.1", true, ""},
		{"empty", "", false, `invalid cluster version "": Invalid Semantic Version`},
		{"whitespace", " ", false, `invalid cluster version " ": Invalid Semantic Version`},
		{"padded release", " v1.0.2 ", false, `invalid cluster version " v1.0.2 ": Invalid Semantic Version`},
		{"escaped whitespace", "\tv1.0.2\n", false, `invalid cluster version "\tv1.0.2\n": Invalid Semantic Version`},
		{"invalid", "invalid", false, `invalid cluster version "invalid": Invalid Semantic Version`},
	}

	for _, tc := range cases {
		test.Run(tc.name, func(test *testing.T) {
			got, err := IsStaticNodeClusterFlowVersion(tc.version)
			if got != tc.want {
				test.Errorf("IsStaticNodeClusterFlowVersion(%q) = %t, want %t", tc.version, got, tc.want)
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

func TestIsSelectableClusterVersion(test *testing.T) {
	cases := []struct {
		name    string
		version string
		want    bool
		wantErr string
	}{
		{"older release", "v1.0.0", false, ""},
		{"gate release", "v1.0.1", false, ""},
		{"gate without prefix", "1.0.1", false, ""},
		{"gate RC", "v1.0.1-rc.1", false, ""},
		{"gate nightly", "v1.0.1-nightly.20261021", false, ""},
		{"gate build metadata", "v1.0.1+build.1", false, ""},
		{"newer release", "v1.0.2", true, ""},
		{"newer without prefix", "1.0.2", true, ""},
		{"newer RC", "v1.0.2-rc.1", true, ""},
		{"newer nightly", "v1.0.2-nightly.20261021", true, ""},
		{"newer build metadata", "v1.0.2+build.1", true, ""},
		{"short version", "v1.1", true, ""},
		{"empty", "", false, "Invalid Semantic Version"},
		{"whitespace", " ", false, "Invalid Semantic Version"},
		{"padded release", " v1.0.2 ", false, "Invalid Semantic Version"},
		{"invalid", "invalid", false, "Invalid Semantic Version"},
	}

	for _, tc := range cases {
		test.Run(tc.name, func(test *testing.T) {
			got, err := IsSelectableClusterVersion(tc.version)
			if got != tc.want {
				test.Errorf("IsSelectableClusterVersion(%q) = %t, want %t", tc.version, got, tc.want)
			}
			if tc.wantErr == "" {
				if err != nil {
					test.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || err.Error() != tc.wantErr || err != mastermindssemver.ErrInvalidSemVer {
				test.Errorf("error = %v, want unwrapped %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidateDesiredClusterVersion(test *testing.T) {
	cases := []struct {
		name    string
		version string
		wantErr string
	}{
		{"release", "v1.1.0", ""},
		{"without prefix", "1.1.0", ""},
		{"RC", "v1.1.0-rc.1", ""},
		{"nightly", "v1.1.0-nightly.20261021", ""},
		{"build metadata", "v1.1.0+build.1", ""},
		{"RC with build metadata", "v1.1.0-rc.1+build.1", ""},
		{"short version", "v1.1", ""},
		{"major version", "1", ""},
		{"empty", "", `invalid desired cluster version "": Invalid Semantic Version`},
		{"whitespace", " ", `invalid desired cluster version " ": Invalid Semantic Version`},
		{"padded release", " v1.1.0 ", `invalid desired cluster version " v1.1.0 ": Invalid Semantic Version`},
		{"invalid", "invalid", `invalid desired cluster version "invalid": Invalid Semantic Version`},
	}

	for _, tc := range cases {
		test.Run(tc.name, func(test *testing.T) {
			err := ValidateDesiredClusterVersion(tc.version)
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

func TestValidateClusterVersionUpgrade(test *testing.T) {
	cases := []struct {
		name    string
		current string
		desired string
	}{
		{"same release", "v1.1.0", "v1.1.0"},
		{"same without prefix", "v1.1.0", "1.1.0"},
		{"short baseline", "v1.1", "v1.1.0"},
		{"patch upgrade", "v1.1.0", "v1.1.1"},
		{"minor upgrade", "v1.1.0", "v1.2.0"},
		{"upgrade to next RC", "v1.1.0", "v1.1.1-rc.1"},
		{"upgrade RC", "v1.1.0-rc.1", "v1.1.0-rc.2"},
		{"RC to release", "v1.1.0-rc.1", "v1.1.0"},
		{"upgrade nightly", "v1.1.0-nightly.20261020", "v1.1.0-nightly.20261021"},
		{"same build metadata", "v1.1.0+build.1", "v1.1.0+build.1"},
		{"ignore build ordering", "v1.1.0+build.2", "v1.1.0+build.1"},
		{"add build metadata", "v1.1.0", "v1.1.0+build.1"},
		{"remove build metadata", "v1.1.0+build.1", "v1.1.0"},
		{"empty baseline", "", "v0.1.0"},
		{"invalid baseline", "invalid", "v0.1.0"},
		{"whitespace baseline", " ", "v0.1.0"},
		{"padded baseline", " v9.0.0 ", "v0.1.0"},
		{"invalid baseline with RC", "invalid", "v0.1.0-rc.1"},
		{"empty baseline with nightly", "", "v0.1.0-nightly.20261021"},
		{"empty baseline with build metadata", "", "v0.1.0+build.1"},
		{"empty baseline without prefix", "", "0.1.0"},
	}

	for _, tc := range cases {
		test.Run(tc.name, func(test *testing.T) {
			if err := ValidateClusterVersionUpgrade(tc.current, tc.desired); err != nil {
				test.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateClusterVersionUpgradeDowngrade(test *testing.T) {
	cases := []struct {
		name    string
		current string
		desired string
		wantErr string
	}{
		{
			"patch downgrade", "v1.1.1", "v1.1.0",
			"cluster version downgrade is not supported: current version is v1.1.1, desired version is v1.1.0",
		},
		{
			"minor downgrade without prefix", "1.2.0", "1.1.9",
			"cluster version downgrade is not supported: current version is 1.2.0, desired version is 1.1.9",
		},
		{
			"release to RC downgrade", "v1.1.0", "v1.1.0-rc.1",
			"cluster version downgrade is not supported: current version is v1.1.0, desired version is v1.1.0-rc.1",
		},
		{
			"RC downgrade", "v1.1.0-rc.2", "v1.1.0-rc.1",
			"cluster version downgrade is not supported: current version is v1.1.0-rc.2, desired version is v1.1.0-rc.1",
		},
		{
			"release to nightly downgrade", "v1.1.0", "v1.1.0-nightly.20261021",
			"cluster version downgrade is not supported: current version is v1.1.0, desired version is v1.1.0-nightly.20261021",
		},
	}

	for _, tc := range cases {
		test.Run(tc.name, func(test *testing.T) {
			err := ValidateClusterVersionUpgrade(tc.current, tc.desired)
			if err == nil || err.Error() != tc.wantErr {
				test.Fatalf("error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidateClusterVersionUpgradeInvalidDesired(test *testing.T) {
	cases := []struct {
		name    string
		current string
		desired string
		wantErr string
	}{
		{"empty desired", "v1.1.0", "", `invalid desired cluster version "": Invalid Semantic Version`},
		{"whitespace desired", "v1.1.0", " ", `invalid desired cluster version " ": Invalid Semantic Version`},
		{"padded desired", "v1.1.0", " v1.2.0 ", `invalid desired cluster version " v1.2.0 ": Invalid Semantic Version`},
		{"invalid desired", "v1.1.0", "invalid", `invalid desired cluster version "invalid": Invalid Semantic Version`},
		{"empty baseline with invalid desired", "", "invalid", `invalid desired cluster version "invalid": Invalid Semantic Version`},
		{"invalid baseline with empty desired", "invalid", "", `invalid desired cluster version "": Invalid Semantic Version`},
		{"same invalid string", "invalid", "invalid", `invalid desired cluster version "invalid": Invalid Semantic Version`},
		{"same empty string", "", "", `invalid desired cluster version "": Invalid Semantic Version`},
		{"same whitespace string", " ", " ", `invalid desired cluster version " ": Invalid Semantic Version`},
	}

	for _, tc := range cases {
		test.Run(tc.name, func(test *testing.T) {
			err := ValidateClusterVersionUpgrade(tc.current, tc.desired)
			if err == nil || err.Error() != tc.wantErr {
				test.Fatalf("error = %v, want %q", err, tc.wantErr)
			}
			if !errors.Is(err, mastermindssemver.ErrInvalidSemVer) {
				test.Errorf("error does not wrap semantic version parse failure: %v", err)
			}
		})
	}
}
