package util

import (
	"fmt"

	"github.com/google/go-containerregistry/pkg/name"

	internalutil "github.com/neutree-ai/neutree/internal/util"
)

func ReplaceImageRegistry(imageURL, mirrorRegistry string) (string, error) {
	if mirrorRegistry == "" {
		return imageURL, nil
	}

	normalizedRegistry := internalutil.StripRegistryScheme(mirrorRegistry)

	ref, err := name.ParseReference(imageURL)
	if err != nil {
		return "", err
	}

	repo := ref.Context()

	newRefStr := fmt.Sprintf("%s/%s", normalizedRegistry, repo.RepositoryStr())
	if tag, ok := ref.(name.Tag); ok {
		newRefStr = fmt.Sprintf("%s:%s", newRefStr, tag.TagStr())
	} else if digest, ok := ref.(name.Digest); ok {
		newRefStr = fmt.Sprintf("%s@%s", newRefStr, digest.DigestStr())
	}

	return newRefStr, nil
}
