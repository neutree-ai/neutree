package metricsconfig

import "testing"

func TestTargetRegistryReplaceAndSnapshot(t *testing.T) {
	registry := NewRegistry()
	registry.Replace("operator/nvidia", []Target{{
		JobName:  "dcgm",
		Selector: map[string]string{"app": "dcgm"},
	}})
	registry.Replace("legacy/amd", []Target{{JobName: "amd"}})

	snapshot := registry.Snapshot()
	if len(snapshot.Targets) != 2 || snapshot.Targets[0].Owner != "legacy/amd" {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}

	registry.Replace("operator/nvidia", nil)
	snapshot = registry.Snapshot()
	if len(snapshot.Targets) != 1 || snapshot.Targets[0].Owner != "legacy/amd" {
		t.Fatalf("stale owner was not removed: %#v", snapshot)
	}
}

func TestTargetRegistryCopiesMutableFields(t *testing.T) {
	registry := NewRegistry()
	selector := map[string]string{"app": "dcgm"}
	registry.Replace("owner", []Target{{JobName: "dcgm", Selector: selector}})
	selector["app"] = "changed"

	got := registry.Snapshot().Targets[0]
	if got.Selector["app"] != "dcgm" {
		t.Fatalf("registry retained caller map: %#v", got.Selector)
	}
}
