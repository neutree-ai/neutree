package packageimport

import (
	"context"
	stderrors "errors"
	"net/http"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/match"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	"github.com/pkg/errors"
)

func multiArchPlatform(value string) (v1.Platform, error) {
	if value == "" {
		return v1.Platform{}, errors.New("platform is required for multi-architecture package import")
	}

	parts := strings.Split(value, "/")
	if len(parts) != 2 || parts[0] != "linux" || (parts[1] != "amd64" && parts[1] != "arm64") {
		return v1.Platform{}, errors.Errorf("unsupported platform %q for multi-architecture package import", value)
	}

	return v1.Platform{OS: parts[0], Architecture: parts[1]}, nil
}

func multiArchChildTarget(target string, platform v1.Platform) (string, error) {
	tag, err := name.NewTag(target)
	if err != nil {
		return "", errors.Wrapf(err, "failed to parse target image %s", target)
	}

	parts := strings.Split(tag.Context().RepositoryStr(), "/")
	parts[len(parts)-1] += "-" + platform.Architecture

	return tag.Context().RegistryStr() + "/" + strings.Join(parts, "/") + ":" + tag.TagStr(), nil
}

func publishMultiArchIndex(ctx context.Context, logicalRef, childRef name.Tag, platform v1.Platform, username, password string) error {
	remoteOptions := []remote.Option{
		remote.WithContext(ctx),
		remote.WithAuth(&authn.Basic{Username: username, Password: password}),
	}

	childImage, err := remote.Image(childRef, remoteOptions...)
	if err != nil {
		return errors.Wrapf(err, "failed to read pushed child image %s", childRef.Name())
	}

	childPlatform, err := verifyImagePlatform(childImage, platform, childRef.Name())
	if err != nil {
		return err
	}

	base, err := existingIndex(logicalRef, remoteOptions...)
	if err != nil {
		return err
	}

	index := mutate.RemoveManifests(base, platformMatcher(platform))
	index = mutate.AppendManifests(index, mutate.IndexAddendum{
		Add:        childImage,
		Descriptor: v1.Descriptor{Platform: &childPlatform},
	})

	if err := remote.WriteIndex(logicalRef, index, remoteOptions...); err != nil {
		return errors.Wrapf(err, "failed to publish multi-architecture index %s", logicalRef.Name())
	}

	return verifyPublishedIndex(logicalRef, childImage, childPlatform, remoteOptions...)
}

func existingIndex(ref name.Tag, options ...remote.Option) (v1.ImageIndex, error) {
	desc, err := remote.Get(ref, options...)
	if err != nil {
		if isRegistryNotFound(err) {
			return empty.Index, nil
		}

		return nil, errors.Wrapf(err, "failed to read existing image %s", ref.Name())
	}

	if desc.MediaType.IsIndex() {
		index, err := desc.ImageIndex()
		return index, errors.Wrapf(err, "failed to read existing image index %s", ref.Name())
	}

	image, err := desc.Image()
	if err != nil {
		return nil, errors.Wrapf(err, "failed to read existing image manifest %s", ref.Name())
	}

	legacyPlatform, err := imagePlatform(image, ref.Name())
	if err != nil {
		return nil, err
	}

	if legacyPlatform.OS != "linux" || (legacyPlatform.Architecture != "amd64" && legacyPlatform.Architecture != "arm64") {
		return nil, errors.Errorf("existing image %s has unsupported platform %s/%s", ref.Name(), legacyPlatform.OS, legacyPlatform.Architecture)
	}

	return mutate.AppendManifests(empty.Index, mutate.IndexAddendum{
		Add:        image,
		Descriptor: v1.Descriptor{Platform: &legacyPlatform},
	}), nil
}

func verifyImagePlatform(image v1.Image, expected v1.Platform, reference string) (v1.Platform, error) {
	actual, err := imagePlatform(image, reference)
	if err != nil {
		return v1.Platform{}, err
	}

	if !actual.Satisfies(expected) {
		return v1.Platform{}, errors.Errorf("pushed image %s has platform %s/%s, expected %s/%s", reference,
			actual.OS, actual.Architecture, expected.OS, expected.Architecture)
	}

	return actual, nil
}

func platformMatcher(platform v1.Platform) match.Matcher {
	return func(descriptor v1.Descriptor) bool {
		return descriptor.Platform != nil && descriptor.Platform.Satisfies(platform)
	}
}

func imagePlatform(image v1.Image, reference string) (v1.Platform, error) {
	config, err := image.ConfigFile()
	if err != nil {
		return v1.Platform{}, errors.Wrapf(err, "failed to read image config for %s", reference)
	}

	if config == nil || config.Platform() == nil {
		return v1.Platform{}, errors.Errorf("image %s does not declare a platform", reference)
	}

	return *config.Platform(), nil
}

func verifyPublishedIndex(ref name.Tag, child v1.Image, platform v1.Platform, options ...remote.Option) error {
	desc, err := remote.Get(ref, options...)
	if err != nil {
		return errors.Wrapf(err, "failed to verify multi-architecture index %s", ref.Name())
	}

	if !desc.MediaType.IsIndex() {
		return errors.Errorf("published image %s is not an OCI index", ref.Name())
	}

	index, err := desc.ImageIndex()
	if err != nil {
		return errors.Wrapf(err, "failed to read published index %s", ref.Name())
	}

	manifest, err := index.IndexManifest()
	if err != nil {
		return errors.Wrapf(err, "failed to read published index manifest %s", ref.Name())
	}

	childDigest, err := child.Digest()
	if err != nil {
		return errors.Wrapf(err, "failed to calculate child image digest for %s", ref.Name())
	}

	for _, descriptor := range manifest.Manifests {
		if descriptor.Platform != nil && descriptor.Platform.Equals(platform) && descriptor.Digest == childDigest {
			return nil
		}
	}

	return errors.Errorf("published index %s does not contain %s/%s child digest %s", ref.Name(), platform.OS, platform.Architecture, childDigest)
}

func isRegistryNotFound(err error) bool {
	var registryError *transport.Error
	return stderrors.As(err, &registryError) && registryError.StatusCode == http.StatusNotFound
}
