package cluster

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStaticNodeDockerRuntimePullImageReturnsDockerReason(t *testing.T) {
	runner := &fakeStaticNodeRunner{
		responses: []fakeStaticNodeResponse{
			{
				command: "docker pull 'registry.example.com/neutree/serve:v1.2.0'",
				err:     errors.New("pull denied"),
			},
		},
	}

	err := NewStaticNodeDockerRuntime(runner).PullImage(
		context.Background(),
		"registry.example.com/neutree/serve:v1.2.0",
	)

	require.Error(t, err)
	assert.Equal(t, componentReasonImagePullFailed, staticNodeDockerReason(err, ""))
	assert.Contains(t, err.Error(), "failed to pull image registry.example.com/neutree/serve:v1.2.0")
}
