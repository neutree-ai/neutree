package image

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type tagCall struct {
	source string
	target string
}

type fakeDocker struct {
	loadBody    string
	loadErr     error
	loadedBytes []byte

	tagCalls []tagCall
	tagErr   error

	pushRef     string
	pushOptions image.PushOptions
	pushBody    string
	pushErr     error
}

func (f *fakeDocker) ImageLoad(_ context.Context, input io.Reader, _ ...client.ImageLoadOption) (image.LoadResponse, error) {
	if f.loadErr != nil {
		return image.LoadResponse{}, f.loadErr
	}

	loaded, err := io.ReadAll(input)
	if err != nil {
		return image.LoadResponse{}, err
	}

	f.loadedBytes = loaded

	return image.LoadResponse{Body: io.NopCloser(bytes.NewBufferString(f.loadBody))}, nil
}

func (f *fakeDocker) ImageTag(_ context.Context, source, target string) error {
	f.tagCalls = append(f.tagCalls, tagCall{source: source, target: target})
	return f.tagErr
}

func (f *fakeDocker) ImagePush(_ context.Context, ref string, options image.PushOptions) (io.ReadCloser, error) {
	f.pushRef = ref
	f.pushOptions = options

	if f.pushErr != nil {
		return nil, f.pushErr
	}

	return io.NopCloser(bytes.NewBufferString(f.pushBody)), nil
}

func TestLoadArchive(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "image.tar")
	require.NoError(t, os.WriteFile(archive, []byte("tarball"), 0o600))

	out := &bytes.Buffer{}
	docker := &fakeDocker{loadBody: `{"stream":"Loaded image: my-workload:v1\n"}`}
	pusher := &Pusher{docker: docker, out: out}

	require.NoError(t, pusher.LoadArchive(context.Background(), archive))
	assert.Equal(t, []byte("tarball"), docker.loadedBytes, "the archive must reach the daemon")
	assert.Contains(t, out.String(), "Loaded image: my-workload:v1")
}

func TestLoadArchiveMissingFile(t *testing.T) {
	pusher := &Pusher{docker: &fakeDocker{}, out: io.Discard}

	err := pusher.LoadArchive(context.Background(), filepath.Join(t.TempDir(), "missing.tar"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to open image archive")
}

func TestTagAndPush(t *testing.T) {
	out := &bytes.Buffer{}
	docker := &fakeDocker{pushBody: `{"status":"Pushed"}`}
	pusher := &Pusher{docker: docker, out: out}

	err := pusher.TagAndPush(context.Background(), "my-workload:v1", "registry.example.com/neutree/my-workload:v1", "auth-blob")
	require.NoError(t, err)

	assert.Equal(t, []tagCall{{source: "my-workload:v1", target: "registry.example.com/neutree/my-workload:v1"}}, docker.tagCalls)
	assert.Equal(t, "registry.example.com/neutree/my-workload:v1", docker.pushRef)
	assert.Equal(t, "auth-blob", docker.pushOptions.RegistryAuth)
	assert.Contains(t, out.String(), "Pushed")
}

func TestTagAndPushMissingLocalImage(t *testing.T) {
	docker := &fakeDocker{tagErr: errors.New("No such image")}
	pusher := &Pusher{docker: docker, out: io.Discard}

	err := pusher.TagAndPush(context.Background(), "my-workload:v1", "registry.example.com/my-workload:v1", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is the image present in the local Docker daemon?")
	assert.Empty(t, docker.pushRef)
}

func TestTagAndPushReportsRegistryError(t *testing.T) {
	docker := &fakeDocker{pushBody: `{"errorDetail":{"message":"unauthorized"},"error":"unauthorized"}`}
	pusher := &Pusher{docker: docker, out: io.Discard}

	err := pusher.TagAndPush(context.Background(), "my-workload:v1", "registry.example.com/my-workload:v1", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unauthorized")
}
