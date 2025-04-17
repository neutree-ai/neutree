package util

import (
	"fmt"

	"github.com/google/go-containerregistry/pkg/name"
)

func ReplaceImageRegistry(imageURL, mirrorRegistry string) (string, error) {
	if mirrorRegistry == "" {
		return imageURL, nil
	}

	ref, err := name.ParseReference(imageURL)
	if err != nil {
		return "", err
	}

	repo := ref.Context()

	newRefStr := fmt.Sprintf("%s/%s", mirrorRegistry, repo.RepositoryStr())
	if tag, ok := ref.(name.Tag); ok {
		newRefStr = fmt.Sprintf("%s:%s", newRefStr, tag.TagStr())
	} else if digest, ok := ref.(name.Digest); ok {
		newRefStr = fmt.Sprintf("%s@%s", newRefStr, digest.DigestStr())
	}

	return newRefStr, nil
}
