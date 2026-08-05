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
