package cluster

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	registryClient "github.com/neutree-ai/neutree/pkg/registry"
	"github.com/stretchr/testify/assert"
)

// fake client to record pushes
type fakeRegistryClient struct {
	pushed []string
	called bool
}

func (f *fakeRegistryClient) PushImageTarToRegistry(ctx context.Context, tarPath string, targetRegistry string) error {
	f.called = true
	f.pushed = append(f.pushed, tarPath)
	return nil
}

func (f *fakeRegistryClient) PushImageTarToRegistryWithAuth(ctx context.Context, tarPath string, targetRegistry string, username, password string, retryCount int) error {
	f.called = true
	f.pushed = append(f.pushed, tarPath)
	return nil
}

func TestClusterImportCmd_UsesRegistryClient(t *testing.T) {
	// create a temp offline package tar.gz that contains a manifest.json and a fake image tar
	tmpDir := t.TempDir()
	pkgPath := filepath.Join(tmpDir, "offline.tar.gz")
	f, err := os.Create(pkgPath)
	assert.NoError(t, err)
	gw := gzip.NewWriter(f)
	tr := tar.NewWriter(gw)

	// write fake image file entry
	imgContent := []byte("fake image payload")
	h := &tar.Header{Name: "myimage.tar", Size: int64(len(imgContent))}
	assert.NoError(t, tr.WriteHeader(h))
	_, err = tr.Write(imgContent)
	assert.NoError(t, err)

	// write package manifest.json referencing the image
	pm := struct {
		Images []struct {
			File     string   `json:"file"`
			RepoTags []string `json:"repoTags"`
		} `json:"images"`
	}{
		Images: []struct {
			File     string   `json:"file"`
			RepoTags []string `json:"repoTags"`
		}{
			{File: "myimage.tar", RepoTags: []string{"repo/myimage:latest"}},
		},
	}
	pmBytes, _ := json.Marshal(pm)
	h = &tar.Header{Name: "manifest.json", Size: int64(len(pmBytes))}
	assert.NoError(t, tr.WriteHeader(h))
	_, err = tr.Write(pmBytes)
	assert.NoError(t, err)

	// close writers
	assert.NoError(t, tr.Close())
	assert.NoError(t, gw.Close())
	assert.NoError(t, f.Close())

	// inject fake registry client
	fake := &fakeRegistryClient{pushed: []string{}}
	old := newRegistryClient
	newRegistryClient = func() registryClient.RegistryClient { return fake }
	defer func() { newRegistryClient = old }()

	cmd := NewClusterImportCmd()
	// set flags
	assert.NoError(t, cmd.Flags().Set("offline-image", pkgPath))
	assert.NoError(t, cmd.Flags().Set("registry", "my.registry.com"))

	err = cmd.RunE(cmd, []string{})
	assert.NoError(t, err)
	assert.True(t, fake.called)
	assert.Len(t, fake.pushed, 1)
}

func TestClusterImportCmd_NoManifest_UsesRegistryClient(t *testing.T) {
	tmpDir := t.TempDir()
	pkgPath := filepath.Join(tmpDir, "offline2.tar.gz")
	f, err := os.Create(pkgPath)
	assert.NoError(t, err)
	gw := gzip.NewWriter(f)
	tr := tar.NewWriter(gw)

	imgContent := []byte("another image")
	h := &tar.Header{Name: "another.tar", Size: int64(len(imgContent))}
	err = tr.WriteHeader(h)
	assert.NoError(t, err)
	_, err = tr.Write(imgContent)
	assert.NoError(t, err)

	assert.NoError(t, tr.Close())
	assert.NoError(t, gw.Close())
	assert.NoError(t, f.Close())

	fake := &fakeRegistryClient{pushed: []string{}}
	old := newRegistryClient
	newRegistryClient = func() registryClient.RegistryClient { return fake }
	defer func() { newRegistryClient = old }()

	cmd := NewClusterImportCmd()
	assert.NoError(t, cmd.Flags().Set("offline-image", pkgPath))
	assert.NoError(t, cmd.Flags().Set("registry", "my.registry.com"))

	err = cmd.RunE(cmd, []string{})
	assert.NoError(t, err)
	assert.True(t, fake.called)
	assert.Len(t, fake.pushed, 1)
}
