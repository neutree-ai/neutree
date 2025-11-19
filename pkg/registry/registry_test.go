package registry

import (
    "archive/tar"
    "bytes"
    // gzip not needed in this test
    "encoding/json"
    "os"
    "path/filepath"
    "testing"

    "github.com/stretchr/testify/assert"
)

// Test parseRepoTags by constructing a tarball with manifest.json
func TestParseRepoTags(t *testing.T) {
    tmpDir := t.TempDir()
    tarPath := filepath.Join(tmpDir, "image.tar")

    manifest := []map[string]interface{}{{
        "RepoTags": []string{"myorg/myimage:1.2.3"},
    }}

    var buf bytes.Buffer
    tw := tar.NewWriter(&buf)

    // add manifest.json
    manifestBytes, err := json.Marshal(manifest)
    if err != nil {
        t.Fatal(err)
    }
    if err := tw.WriteHeader(&tar.Header{Name: "manifest.json", Size: int64(len(manifestBytes))}); err != nil {
        t.Fatal(err)
    }
    if _, err := tw.Write(manifestBytes); err != nil {
        t.Fatal(err)
    }

    // add a dummy layer file
    layerContent := []byte("dummy")
    if err := tw.WriteHeader(&tar.Header{Name: "layer.tar", Size: int64(len(layerContent))}); err != nil {
        t.Fatal(err)
    }
    if _, err := tw.Write(layerContent); err != nil {
        t.Fatal(err)
    }

    tw.Close()

    // write tar to disk
    if err := os.WriteFile(tarPath, buf.Bytes(), 0644); err != nil {
        t.Fatalf("write tar: %v", err)
    }

    // parse repo tags
    tags, err := parseRepoTags(tarPath)
    assert.NoError(t, err)
    assert.Equal(t, []string{"myorg/myimage:1.2.3"}, tags)
}

func TestParsePackageManifest(t *testing.T) {
    tmpDir := t.TempDir()
    manifestPath := filepath.Join(tmpDir, "manifest.json")
    pm := packageManifest{Images: []struct {
        File     string   `json:"file"`
        RepoTags []string `json:"repoTags"`
    }{{File: "neutree-ssh-cluster.tar", RepoTags: []string{"neutree-ssh-cluster:latest"}}}}

    b, err := json.Marshal(pm)
    if err != nil {
        t.Fatal(err)
    }

    if err := os.WriteFile(manifestPath, b, 0644); err != nil {
        t.Fatal(err)
    }

    pm2, err := ParsePackageManifest(manifestPath)
    assert.NoError(t, err)
    assert.Len(t, pm2.Images, 1)
    assert.Equal(t, "neutree-ssh-cluster.tar", pm2.Images[0].File)
}
