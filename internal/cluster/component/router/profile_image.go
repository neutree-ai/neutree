package router

import (
	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
)

// BuildProfileImage resolves the Router image from the exact Kubernetes
// ClusterProfile component matrix.
func BuildProfileImage(imagePrefix string, image v1.ImageRef) (string, error) {
	resolved, err := util.BuildProfileImageRef(imagePrefix, "router", image)
	if err != nil {
		return "", errors.Wrap(err, "build router image from cluster profile")
	}

	return resolved, nil
}
