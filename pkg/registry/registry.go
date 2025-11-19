package registry

import (
    "archive/tar"
    "context"
    "encoding/json"
    "fmt"
    "io"
    "os"
    "path/filepath"
    "strings"

    // v1 is available from go-containerregistry; not needed here directly but kept for reference
    "github.com/google/go-containerregistry/pkg/v1/remote"
    "github.com/google/go-containerregistry/pkg/v1/tarball"
    "github.com/google/go-containerregistry/pkg/name"
    "github.com/google/go-containerregistry/pkg/authn"
    "time"
    "github.com/pkg/errors"
)

type RegistryClient interface {
    // PushImageTarToRegistry reads a docker-save tar file and pushes included images to targetRegistry.
    // Uses the default keychain / no inline auth.
    PushImageTarToRegistry(ctx context.Context, tarPath string, targetRegistry string) error

    // PushImageTarToRegistryWithAuth reads a docker-save tar file and pushes included images to targetRegistry.
    // username/password may be empty to use the default keychain. retryCount specifies number of attempts.
    PushImageTarToRegistryWithAuth(ctx context.Context, tarPath string, targetRegistry string, username, password string, retryCount int) error
}

type DefaultRegistryClient struct{}

func NewDefaultRegistryClient() *DefaultRegistryClient {
    return &DefaultRegistryClient{}
}

// manifestEntry corresponds to an object in manifest.json written by docker save
type manifestEntry struct {
    RepoTags []string `json:"RepoTags"`
}

// packageManifest is the top-level manifest placed into the offline image package.
type packageManifest struct {
    Images []struct {
        File     string   `json:"file"`
        RepoTags []string `json:"repoTags"`
    } `json:"images"`
}

// ParsePackageManifest parses a package-level manifest.json file (not the docker manifest inside tar).
func ParsePackageManifest(manifestPath string) (*packageManifest, error) {
    f, err := os.Open(manifestPath)
    if err != nil {
        return nil, err
    }
    defer f.Close()

    var pm packageManifest
    dec := json.NewDecoder(f)
    if err := dec.Decode(&pm); err != nil {
        return nil, err
    }
    return &pm, nil
}

// parseRepoTags reads manifest.json inside docker-save tar file and returns repo tags found
func parseRepoTags(tarPath string) ([]string, error) {
    f, err := os.Open(tarPath)
    if err != nil {
        return nil, err
    }
    defer f.Close()

    tr := tar.NewReader(f)
    for {
        hdr, err := tr.Next()
        if err == io.EOF {
            break
        }
        if err != nil {
            return nil, err
        }
        if filepath.Base(hdr.Name) == "manifest.json" {
            var entries []manifestEntry
            dec := json.NewDecoder(tr)
            if err := dec.Decode(&entries); err != nil {
                return nil, err
            }
            if len(entries) > 0 {
                return entries[0].RepoTags, nil
            }
            return nil, nil
        }
    }
    return nil, fmt.Errorf("manifest.json not found in tar: %s", tarPath)
}

// ParseRepoTagsFromTar is an exported wrapper for parseRepoTags to be used by callers that
// want to inspect repo tags available inside a docker-save tar file.
func ParseRepoTagsFromTar(tarPath string) ([]string, error) {
    return parseRepoTags(tarPath)
}

// PushImageTarToRegistry pushes images contained in a docker-save tar to the specified registry.
// It will rewrite the repository host to the provided targetRegistry and preserve the repo path and tag.
func (c *DefaultRegistryClient) PushImageTarToRegistry(ctx context.Context, tarPath string, targetRegistry string) error {
    return c.PushImageTarToRegistryWithAuth(ctx, tarPath, targetRegistry, "", "", 3)
}

// PushImageTarToRegistryWithAuth supports username/password auth and retry.
func (c *DefaultRegistryClient) PushImageTarToRegistryWithAuth(ctx context.Context, tarPath string, targetRegistry string, username, password string, retryCount int) error {
    repoTags, err := parseRepoTags(tarPath)
    if err != nil {
        return errors.Wrap(err, "parse manifest")
    }

    // If no repo tags, nothing to push
    if len(repoTags) == 0 {
        return nil
    }

    // Load image from tarball (first image) — docker save may have multiple images but usually one
    img, err := tarball.ImageFromPath(tarPath, nil)
    if err != nil {
        return errors.Wrap(err, "load image from tar")
    }

    for _, repoTag := range repoTags {
        // repoTag looks like "library/nginx:latest" or "myrepo/myimage:tag"
        // Build new reference: targetRegistry/<repoTag>
        parts := strings.SplitN(repoTag, ":", 2)
        repoPath := parts[0]
        tag := "latest"
        if len(parts) > 1 {
            tag = parts[1]
        }

        // remove registry part if any
        shortRepo := repoPath
        if strings.Contains(repoPath, "/") {
            // if repo has host part (like index.docker.io/library/nginx), keep only path after host
            // naive split: if host contains "." or ":" treat first part as host
            p := strings.SplitN(repoPath, "/", 2)
            if strings.Contains(p[0], ".") || strings.Contains(p[0], ":") {
                if len(p) > 1 {
                    shortRepo = p[1]
                }
            }
        }

        targetRef := fmt.Sprintf("%s/%s:%s", targetRegistry, shortRepo, tag)
        ref, err := name.ParseReference(targetRef)
        if err != nil {
            return errors.Wrapf(err, "parse reference %s", targetRef)
        }

        var auth authn.Authenticator
        if username != "" || password != "" {
            auth = authn.FromConfig(authn.AuthConfig{Username: username, Password: password})
        } else {
            // authn.DefaultKeychain is not an authenticator itself; use the default keychain via WithAuthFromKeychain
            auth = nil
        }

        // retry with backoff
        var lastErr error
        for attempt := 0; attempt < retryCount; attempt++ {
            var writeErr error
            if auth == nil {
                writeErr = remote.Write(ref, img, remote.WithAuthFromKeychain(authn.DefaultKeychain))
            } else {
                writeErr = remote.Write(ref, img, remote.WithAuth(auth))
            }

            if writeErr == nil {
                lastErr = nil
                break
            }

            lastErr = writeErr
            // exponential backoff: 1s, 2s, 3s...
            backoff := time.Duration(attempt+1) * time.Second
            time.Sleep(backoff)
        }
    if lastErr != nil {
            return errors.Wrapf(lastErr, "push image %s", targetRef)
        }
    }

    return nil
}
