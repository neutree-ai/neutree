package controllers

import (
	"testing"
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
				"neutree.ai/force-delete": forceDeleteAnnotationValue,
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
				"neutree.ai/force-delete": forceDeleteAnnotationValue,
				"other-annotation":        "value",
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsForceDelete(tt.annotations); got != tt.want {
				t.Errorf("IsForceDelete() = %v, want %v", got, tt.want)
			}
		})
	}
}
