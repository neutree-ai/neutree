package cluster

import (
	"context"
	"errors"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStaticNodeReconcilerReconcileWarmImages(t *testing.T) {
	tests := []struct {
		name       string
		node       *v1.StaticNode
		runner     *fakeStaticNodeRunner
		wantReady  bool
		wantErr    bool
		wantImages []v1.WarmImageStatus
	}{
		{
			name: "no warm images is ready",
			node: &v1.StaticNode{Spec: &v1.StaticNodeSpec{}},
			runner: &fakeStaticNodeRunner{
				responses: nil,
			},
			wantReady: true,
		},
		{
			name: "existing required image skips pull",
			node: staticNodeWithWarmImages([]v1.WarmImageSpec{
				{Name: "ray-runtime", Ref: "registry.example.com/neutree/serve:v1.2.0", Required: true},
			}),
			runner: &fakeStaticNodeRunner{
				responses: []fakeStaticNodeResponse{
					{
						command: "docker image inspect --format='{{index .RepoDigests 0}}' 'registry.example.com/neutree/serve:v1.2.0'",
						output:  "registry.example.com/neutree/serve@sha256:ready\n",
					},
				},
			},
			wantReady: true,
			wantImages: []v1.WarmImageStatus{
				{
					Name:   "ray-runtime",
					Ref:    "registry.example.com/neutree/serve:v1.2.0",
					Ready:  true,
					Digest: "registry.example.com/neutree/serve@sha256:ready",
					Phase:  v1.WarmPhaseReady,
					Reason: warmReasonImageReady,
				},
			},
		},
		{
			name: "missing required image pulls then records digest",
			node: staticNodeWithWarmImages([]v1.WarmImageSpec{
				{Name: "ray-runtime", Ref: "registry.example.com/neutree/serve:v1.2.0", Required: true},
			}),
			runner: &fakeStaticNodeRunner{
				responses: []fakeStaticNodeResponse{
					{
						command: "docker image inspect --format='{{index .RepoDigests 0}}' 'registry.example.com/neutree/serve:v1.2.0'",
						err:     errors.New("not found"),
					},
					{
						command: "docker pull 'registry.example.com/neutree/serve:v1.2.0'",
					},
					{
						command: "docker image inspect --format='{{index .RepoDigests 0}}' 'registry.example.com/neutree/serve:v1.2.0'",
						output:  "registry.example.com/neutree/serve@sha256:pulled\n",
					},
				},
			},
			wantReady: true,
			wantImages: []v1.WarmImageStatus{
				{
					Name:   "ray-runtime",
					Ref:    "registry.example.com/neutree/serve:v1.2.0",
					Ready:  true,
					Digest: "registry.example.com/neutree/serve@sha256:pulled",
					Phase:  v1.WarmPhaseReady,
					Reason: warmReasonImagePulled,
				},
			},
		},
		{
			name: "optional image pull failure does not block required warm readiness",
			node: staticNodeWithWarmImages([]v1.WarmImageSpec{
				{Name: "engine", Ref: "registry.example.com/neutree/engine:test", Required: false},
			}),
			runner: &fakeStaticNodeRunner{
				responses: []fakeStaticNodeResponse{
					{
						command: "docker image inspect --format='{{index .RepoDigests 0}}' 'registry.example.com/neutree/engine:test'",
						err:     errors.New("not found"),
					},
					{
						command: "docker pull 'registry.example.com/neutree/engine:test'",
						err:     errors.New("pull denied"),
					},
				},
			},
			wantReady: true,
			wantImages: []v1.WarmImageStatus{
				{
					Name:    "engine",
					Ref:     "registry.example.com/neutree/engine:test",
					Ready:   false,
					Phase:   v1.WarmPhaseFailed,
					Reason:  warmReasonImagePullFailed,
					Message: "failed to pull image registry.example.com/neutree/engine:test: pull denied",
				},
			},
		},
		{
			name: "required image pull failure returns error",
			node: staticNodeWithWarmImages([]v1.WarmImageSpec{
				{Name: "ray-runtime", Ref: "registry.example.com/neutree/serve:v1.2.0", Required: true},
			}),
			runner: &fakeStaticNodeRunner{
				responses: []fakeStaticNodeResponse{
					{
						command: "docker image inspect --format='{{index .RepoDigests 0}}' 'registry.example.com/neutree/serve:v1.2.0'",
						err:     errors.New("not found"),
					},
					{
						command: "docker pull 'registry.example.com/neutree/serve:v1.2.0'",
						err:     errors.New("pull denied"),
					},
				},
			},
			wantReady: false,
			wantErr:   true,
			wantImages: []v1.WarmImageStatus{
				{
					Name:    "ray-runtime",
					Ref:     "registry.example.com/neutree/serve:v1.2.0",
					Ready:   false,
					Phase:   v1.WarmPhaseFailed,
					Reason:  warmReasonImagePullFailed,
					Message: "failed to pull image registry.example.com/neutree/serve:v1.2.0: pull denied",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, err := (&StaticNodeReconciler{}).ReconcileWarmImages(context.Background(), tt.node, tt.runner)

			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.NotNil(t, status)
			assert.Equal(t, tt.wantReady, status.Ready)
			if len(tt.wantImages) > 0 {
				assert.Equal(t, tt.wantImages, status.Images)
			}
			assert.Equal(t, len(tt.runner.responses), tt.runner.calls)
		})
	}
}

func staticNodeWithWarmImages(images []v1.WarmImageSpec) *v1.StaticNode {
	return &v1.StaticNode{
		Spec: &v1.StaticNodeSpec{
			Warm: &v1.WarmSpec{
				Images: images,
			},
		},
	}
}

type fakeStaticNodeRunner struct {
	responses []fakeStaticNodeResponse
	calls     int
}

type fakeStaticNodeResponse struct {
	command string
	output  string
	err     error
}

func (f *fakeStaticNodeRunner) Run(_ context.Context, command string) (string, error) {
	if f.calls >= len(f.responses) {
		return "", errors.New("unexpected command: " + command)
	}

	response := f.responses[f.calls]
	f.calls++

	if response.command != command {
		return "", errors.New("unexpected command: " + command + ", want: " + response.command)
	}

	return response.output, response.err
}
