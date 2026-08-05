package e2e

import "testing"

func TestProfileEngineVersionDefaultsUseMaintainedVLLMVersions(t *testing.T) {
	originalProfile := profile
	profile = Profile{}
	t.Cleanup(func() {
		profile = originalProfile
	})

	if got, want := profileEngineVersion(), "v0.24.0"; got != want {
		t.Errorf("profileEngineVersion() = %q, want %q", got, want)
	}
	if got, want := profileEngineOldVersion(), "v0.17.1"; got != want {
		t.Errorf("profileEngineOldVersion() = %q, want %q", got, want)
	}
}

func TestProfileEngineOldVersionUsesOtherMaintainedVersion(t *testing.T) {
	originalProfile := profile
	profile = Profile{
		Engines: map[string]EngineProfile{
			defaultEngineName: {Version: "v0.17.1"},
		},
	}
	t.Cleanup(func() {
		profile = originalProfile
	})

	if got, want := profileEngineOldVersion(), "v0.24.0"; got != want {
		t.Errorf("profileEngineOldVersion() = %q, want %q", got, want)
	}
}

func TestValidateMaintainedVLLMVersions(t *testing.T) {
	tests := []struct {
		name    string
		profile EngineProfile
		wantErr bool
	}{
		{name: "empty uses defaults"},
		{name: "current version", profile: EngineProfile{Version: "v0.24.0"}},
		{name: "other maintained version", profile: EngineProfile{OldVersion: "v0.17.1"}},
		{name: "retired current version", profile: EngineProfile{Version: "v0.11.2"}, wantErr: true},
		{name: "retired old version", profile: EngineProfile{OldVersion: "v0.8.5"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateMaintainedVLLMVersions(tt.profile)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateMaintainedVLLMVersions() error = %v, wantErr %t", err, tt.wantErr)
			}
		})
	}
}
