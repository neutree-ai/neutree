package model_registry

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/assert"
)

func TestNewHuggingFace(t *testing.T) {
	tests := []struct {
		name          string
		registry      *v1.ModelRegistry
		wantErr       bool
		wantErrString string
	}{
		{
			name: "registry with empty url",
			registry: &v1.ModelRegistry{
				Spec: &v1.ModelRegistrySpec{
					Type: v1.HuggingFaceModelRegistryType,
					Url:  "",
				},
			},
			wantErr:       true,
			wantErrString: "cannot be empty",
		},
		{
			name: "registry with invalid url, no scheme",
			registry: &v1.ModelRegistry{
				Spec: &v1.ModelRegistrySpec{
					Type: v1.HuggingFaceModelRegistryType,
					Url:  "invalid-url",
				},
			},
			wantErr:       true,
			wantErrString: "invalid registry.Spec.Url",
		},
		{
			name: "registry with valid url, no host",
			registry: &v1.ModelRegistry{
				Spec: &v1.ModelRegistrySpec{
					Type: v1.HuggingFaceModelRegistryType,
					Url:  "http://",
				},
			},
			wantErr:       true,
			wantErrString: "invalid registry.Spec.Url",
		},
		{
			name: "registry with valid url, unsupport character",
			registry: &v1.ModelRegistry{
				Spec: &v1.ModelRegistrySpec{
					Type: v1.HuggingFaceModelRegistryType,
					Url: `
					`,
				},
			},
			wantErr:       true,
			wantErrString: "invalid registry.Spec.Url",
		},
		{
			name: "normal registry",
			registry: &v1.ModelRegistry{
				Spec: &v1.ModelRegistrySpec{
					Type: v1.HuggingFaceModelRegistryType,
					Url:  "https://huggingface.co",
				},
			},
			wantErr:       false,
			wantErrString: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := newHuggingFace(tt.registry)
			if tt.wantErr {
				assert.ErrorContains(t, err, tt.wantErrString)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
