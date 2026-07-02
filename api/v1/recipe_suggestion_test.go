package v1

import (
	"encoding/json"
	"testing"
)

// TestRecipeFeatureSuggestionRoundTrip verifies the dual JSON shape: a bare
// string decodes to {Value, no Label} and re-marshals back to a bare string;
// an object preserves the label across a round trip.
func TestRecipeFeatureSuggestionRoundTrip(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		wantValue string
		wantLabel string
		wantOut   string
	}{
		{"bare string", `"8192"`, "8192", "", `"8192"`},
		{"labeled object", `{"value":"8192","label":"8K"}`, "8192", "8K", `{"value":"8192","label":"8K"}`},
		{"object without label", `{"value":"8192"}`, "8192", "", `"8192"`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var s RecipeFeatureSuggestion
			if err := json.Unmarshal([]byte(tc.in), &s); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}

			if s.Value != tc.wantValue || s.Label != tc.wantLabel {
				t.Fatalf("got {%q,%q}, want {%q,%q}", s.Value, s.Label, tc.wantValue, tc.wantLabel)
			}

			out, err := json.Marshal(s)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}

			if string(out) != tc.wantOut {
				t.Fatalf("re-marshal got %s, want %s", out, tc.wantOut)
			}
		})
	}
}
