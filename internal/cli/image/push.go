package image

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/client"
)

// RegistryLister is the subset of client.ImageRegistriesService used here.
type RegistryLister interface {
	List(opts client.ImageRegistryListOptions) ([]v1.ImageRegistry, error)
}

// ImagePusher loads, tags and pushes images; implemented by Pusher.
type ImagePusher interface {
	LoadArchive(ctx context.Context, archivePath string) error
	TagAndPush(ctx context.Context, source, target, registryAuth string) error
}

// PushOptions describes one `neutree-cli image push` invocation.
type PushOptions struct {
	// Workspace holds the image registry resource.
	Workspace string

	// Registry is the name of the Neutree image registry resource to push to.
	Registry string

	// SourceImage is the local image reference to push.
	SourceImage string

	// Target optionally overrides the repository[:tag] inside the target
	// registry. It is resolved against the registry prefix just like
	// SourceImage, so "myorg/app:v1" is enough.
	Target string

	// ArchivePath optionally points at a `docker save` tar to load before pushing.
	ArchivePath string

	// Username and Password override the credentials stored on the image
	// registry resource. Both must be set together.
	Username string
	Password string
}

// Push loads (optionally), tags and pushes SourceImage into the Neutree-managed
// image registry named by the options. The returned reference is the one the
// platform can pull, i.e. the value to use in an engine `image` override.
func Push(ctx context.Context, lister RegistryLister, pusher ImagePusher, opts PushOptions) (string, error) {
	if err := opts.normalizeAndValidate(); err != nil {
		return "", err
	}

	imageRegistry, credentialsDenied, err := resolveRegistry(lister, opts)
	if err != nil {
		return "", err
	}

	host, err := util.GetImageRegistryHost(imageRegistry)
	if err != nil {
		return "", errors.Wrapf(err, "failed to resolve host of image registry %s", opts.Registry)
	}

	prefix, err := util.BuildImagePrefix(host, imageRegistry.Spec.Repository)
	if err != nil {
		return "", errors.Wrapf(err, "failed to resolve image prefix for image registry %s", opts.Registry)
	}

	registryAuth, credSource, err := buildRegistryAuth(imageRegistry, host, opts)
	if err != nil {
		return "", err
	}

	if credentialsDenied {
		credSource = credentialsUnreadable
	}

	source := normalizeReference(opts.SourceImage)

	desired := source
	if opts.Target != "" {
		desired = normalizeReference(opts.Target)
	}

	// Validate before loading, so a bad --target fails in milliseconds rather
	// than after a multi-gigabyte archive import.
	target := util.RelocateImageRef(prefix, desired)
	if _, err := name.ParseReference(target); err != nil {
		return "", errors.Wrapf(err, "resolved target image reference %q is not valid", target)
	}

	if opts.ArchivePath != "" {
		if err := pusher.LoadArchive(ctx, opts.ArchivePath); err != nil {
			return "", err
		}
	}

	if err := pusher.TagAndPush(ctx, source, target, registryAuth); err != nil {
		return "", explainPushFailure(err, credSource, opts.Registry)
	}

	return target, nil
}

// normalizeAndValidate trims the identifier-like fields so validation and
// execution agree on the values — `--image-registry "default "` resolves rather
// than 404s. Credentials and paths are left alone: whitespace can be meaningful
// in a password or a filename.
func (o *PushOptions) normalizeAndValidate() error {
	o.Workspace = strings.TrimSpace(o.Workspace)
	o.Registry = strings.TrimSpace(o.Registry)
	o.SourceImage = strings.TrimSpace(o.SourceImage)
	o.Target = strings.TrimSpace(o.Target)

	if o.SourceImage == "" {
		return errors.New("image reference is required")
	}

	if o.Registry == "" {
		return errors.New("image registry name is required")
	}

	if (o.Username == "") != (o.Password == "") {
		return errors.New("--registry-username and --registry-password must be set together")
	}

	return nil
}

// resolveRegistry reads the image registry, preferring the credentialed view.
// It reports whether the credentials were denied, so the caller can explain a
// later authentication failure. Errors other than "this key may not read
// credentials" are returned as is, so a real outage is never silently
// downgraded to an anonymous push.
func resolveRegistry(lister RegistryLister, opts PushOptions) (*v1.ImageRegistry, bool, error) {
	// Explicit credentials win over the stored ones, so do not fetch a secret
	// that would only be discarded.
	if opts.Username != "" {
		imageRegistry, err := getRegistry(lister, opts, false)
		return imageRegistry, false, err
	}

	imageRegistry, err := getRegistry(lister, opts, true)
	if err == nil {
		return imageRegistry, false, nil
	}

	// 401/403: the key may not read credentials. 404: this control plane has no
	// credentials endpoint. Both are recoverable through the masked read.
	if !client.HasStatus(err, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound) {
		return nil, false, err
	}

	imageRegistry, err = getRegistry(lister, opts, false)

	return imageRegistry, err == nil, err
}

func getRegistry(lister RegistryLister, opts PushOptions, withCreds bool) (*v1.ImageRegistry, error) {
	// The server filters by name and workspace, so this returns 0 or 1 element.
	registries, err := lister.List(client.ImageRegistryListOptions{
		Workspace: opts.Workspace,
		Name:      opts.Registry,
		WithCreds: withCreds,
	})
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get image registry %s", opts.Registry)
	}

	if len(registries) == 0 {
		return nil, errors.Errorf("image registry %s not found in workspace %s", opts.Registry, opts.Workspace)
	}

	return &registries[0], nil
}

// credentialSource records where the push credentials came from, so that an
// authentication failure can name the thing the user has to change.
type credentialSource int

const (
	// credentialsFromRegistry: the registry resource's stored credentials.
	credentialsFromRegistry credentialSource = iota
	// credentialsFromFlags: --registry-username / --registry-password.
	credentialsFromFlags
	// credentialsAnonymous: the registry stores no credentials.
	credentialsAnonymous
	// credentialsUnreadable: the registry may store credentials, but this API
	// key may not read them, so the push went out anonymously.
	credentialsUnreadable
)

// buildRegistryAuth prefers explicit credentials over the stored ones. A
// registry with neither is pushed to anonymously — the platform itself skips
// docker login for those (see controllers/static_node_controller.go).
func buildRegistryAuth(imageRegistry *v1.ImageRegistry, host string, opts PushOptions) (string, credentialSource, error) {
	username, password := opts.Username, opts.Password
	source := credentialsFromFlags

	if username == "" {
		var err error

		username, password, err = util.GetImageRegistryAuthInfo(imageRegistry)
		if err != nil {
			return "", source, errors.Wrap(err, "failed to read image registry credentials")
		}

		source = credentialsFromRegistry
		if username == "" {
			source = credentialsAnonymous
		}
	}

	auth, err := util.EncodeRegistryAuth(username, password, host)

	return auth, source, err
}

// explainPushFailure attaches a hint to an authentication failure, naming what
// the user has to change. Registry credentials in Neutree are only required to
// grant pull, so a valid credential that cannot push is the expected failure,
// not a corner case. Non-authentication failures (a missing local image, a
// broken daemon) are returned untouched — a credential hint would misdirect.
func explainPushFailure(err error, source credentialSource, registry string) error {
	if !isAuthFailure(err) {
		return err
	}

	switch source {
	case credentialsUnreadable:
		return errors.Wrapf(err,
			"pushed anonymously because the credentials of image registry %s could not be read: the API key "+
				"may lack the image_registry:read-credentials permission, pass --registry-username/--registry-password",
			registry)
	case credentialsFromRegistry:
		return errors.Wrapf(err,
			"the stored credentials of image registry %s were rejected: Neutree only requires them to grant pull, "+
				"so update the registry with an account that may push, or pass --registry-username/--registry-password",
			registry)
	case credentialsAnonymous:
		return errors.Wrapf(err,
			"pushed anonymously because image registry %s stores no credentials: add credentials that may push to "+
				"the registry, or pass --registry-username/--registry-password",
			registry)
	case credentialsFromFlags:
		return errors.Wrapf(err,
			"the credentials passed with --registry-username/--registry-password were rejected by image registry %s",
			registry)
	}

	return err
}

// isAuthFailure reports whether err looks like a registry rejecting the
// credentials. The Docker daemon surfaces these as opaque strings from the
// registry's own response, so matching on text is the only option; the phrases
// below are what Docker Registry v2 implementations (including Harbor) return.
func isAuthFailure(err error) bool {
	message := strings.ToLower(err.Error())

	for _, phrase := range []string{
		"denied",
		"unauthorized",
		"authentication required",
		"no basic auth credentials",
		"insufficient_scope",
		"forbidden",
	} {
		if strings.Contains(message, phrase) {
			return true
		}
	}

	return false
}

// normalizeReference appends the implicit :latest tag so the reported target is
// unambiguous. Digest references and already-tagged references are untouched.
func normalizeReference(ref string) string {
	if strings.Contains(ref, "@") {
		return ref
	}

	lastSlash := strings.LastIndex(ref, "/")
	if strings.Contains(ref[lastSlash+1:], ":") {
		return ref
	}

	return ref + ":latest"
}
