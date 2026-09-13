package compatibility

import "testing"

func TestDefaultMatrix(t *testing.T) {
	matrix, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	result, reason := matrix.Check("vllm", "0.24.0", "0.5.0")
	if result != Supported || reason == "" {
		t.Fatalf("supported lookup = %q, %q", result, reason)
	}
	result, reason = matrix.Check("vllm", "0.17.1", "0.5.0")
	if result != Unknown || reason == "" {
		t.Fatalf("unknown lookup = %q, %q", result, reason)
	}
}

func TestLoadRejectsInvalidAndDuplicateRules(t *testing.T) {
	for _, data := range []string{
		"rules:\n  - engine: vllm\n    engineVersion: 0.24.0\n    zcacheVersion: 0.5.0\n    result: maybe\n",
		"rules:\n  - engine: vllm\n    engineVersion: 0.24.0\n    zcacheVersion: 0.5.0\n    result: supported\n  - engine: vllm\n    engineVersion: 0.24.0\n    zcacheVersion: 0.5.0\n    result: supported\n",
	} {
		if _, err := Load([]byte(data)); err == nil {
			t.Fatal("Load accepted invalid matrix")
		}
	}
}
