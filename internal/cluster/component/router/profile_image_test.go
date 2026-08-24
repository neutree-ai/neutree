package router

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildProfileImage(t *testing.T) {
	image, err := BuildProfileImage(
		"registry.example.com/neutree",
		v1.ImageRef{Image: "neutree/router", Tag: "v1.2.1"},
	)

	require.NoError(t, err)
	assert.Equal(t, "registry.example.com/neutree/neutree/router:v1.2.1", image)
}

func TestBuildProfileImageRejectsIncompleteReference(t *testing.T) {
	_, err := BuildProfileImage("registry.example.com/neutree", v1.ImageRef{Image: "neutree/router"})

	require.ErrorContains(t, err, "build router image from cluster profile: router tag is required")
}
