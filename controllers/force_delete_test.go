package controllers

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestIsForceDelete(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
		want        bool
	}{
		{
			name:        "nil annotations",
			annotations: nil,
			want:        false,
		},
		{
			name:        "empty annotations",
			annotations: map[string]string{},
			want:        false,
		},
		{
			name: "force delete true",
			annotations: map[string]string{
				"neutree.ai/force-delete": "true",
			},
			want: true,
		},
		{
			name: "force delete false",
			annotations: map[string]string{
				"neutree.ai/force-delete": "false",
			},
			want: false,
		},
		{
			name: "force delete empty string",
			annotations: map[string]string{
				"neutree.ai/force-delete": "",
			},
			want: false,
		},
		{
			name: "other annotations only",
			annotations: map[string]string{
				"other-annotation": "value",
			},
			want: false,
		},
		{
			name: "force delete with other annotations",
			annotations: map[string]string{
				"neutree.ai/force-delete": "true",
				"other-annotation":        "value",
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := v1.IsForceDelete(tt.annotations); got != tt.want {
				t.Errorf("v1.IsForceDelete() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWithForceDeleteAnnotation(t *testing.T) {
	annotations := map[string]string{"other": "value"}

	got := v1.WithForceDeleteAnnotation(annotations)

	if !v1.IsForceDelete(got) {
		t.Fatalf("v1.WithForceDeleteAnnotation() did not set force-delete annotation")
	}
	if got["other"] != "value" {
		t.Fatalf("v1.WithForceDeleteAnnotation() did not preserve existing annotations")
	}
	if _, mutated := annotations[v1.ForceDeleteAnnotationKey]; mutated {
		t.Fatalf("v1.WithForceDeleteAnnotation() mutated the input annotations")
	}
}
