package logsink

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestFileSyncer(t *testing.T) {
	dir := t.TempDir()
	s := NewFileSyncer(dir)
	ctx := context.Background()

	first := &Rendered{
		Content: []byte("# content-hash: 1\nsinks: {}\n"),
		Secrets: map[string][]byte{"token.a": []byte(`"t"`), "ca.a": []byte("pem")},
	}

	written, err := s.Sync(ctx, first)
	require.NoError(t, err)
	assert.True(t, written)

	got, err := os.ReadFile(filepath.Join(dir, ConfigFileName))
	require.NoError(t, err)
	assert.Equal(t, first.Content, got)

	for name, data := range first.Secrets {
		path := filepath.Join(dir, SecretsDirName, name)
		b, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, data, b)

		info, err := os.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}

	// Same content: nothing is touched.
	before, err := os.Stat(filepath.Join(dir, ConfigFileName))
	require.NoError(t, err)

	written, err = s.Sync(ctx, first)
	require.NoError(t, err)
	assert.False(t, written)

	after, err := os.Stat(filepath.Join(dir, ConfigFileName))
	require.NoError(t, err)
	assert.Equal(t, before.ModTime(), after.ModTime())

	// A stale secret and a leftover temp file are removed.
	require.NoError(t, os.WriteFile(filepath.Join(dir, SecretsDirName, ".token.b.tmp"), []byte("x"), 0o600))

	second := &Rendered{
		Content: []byte("# content-hash: 2\nsinks: {}\n"),
		Secrets: map[string][]byte{"token.a": []byte(`"t2"`)},
	}

	written, err = s.Sync(ctx, second)
	require.NoError(t, err)
	assert.True(t, written)

	entries, err := os.ReadDir(filepath.Join(dir, SecretsDirName))
	require.NoError(t, err)

	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}

	assert.Equal(t, []string{"token.a"}, names)

	b, err := os.ReadFile(filepath.Join(dir, SecretsDirName, "token.a"))
	require.NoError(t, err)
	assert.Equal(t, `"t2"`, string(b))

	// No temp file is left next to the config.
	rootEntries, err := os.ReadDir(dir)
	require.NoError(t, err)

	for _, e := range rootEntries {
		assert.NotContains(t, e.Name(), ".tmp")
	}
}

func TestKubernetesSyncer(t *testing.T) {
	client := fake.NewSimpleClientset()
	s := NewKubernetesSyncer(client, "ns", "cm", "sec")
	ctx := context.Background()

	first := &Rendered{Content: []byte("c1"), Secrets: map[string][]byte{"token.a": []byte(`"t"`)}}

	written, err := s.Sync(ctx, first)
	require.NoError(t, err)
	assert.True(t, written)

	sec, err := client.CoreV1().Secrets("ns").Get(ctx, "sec", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, first.Secrets, sec.Data)

	cm, err := client.CoreV1().ConfigMaps("ns").Get(ctx, "cm", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{ConfigFileName: "c1"}, cm.Data)

	client.ClearActions()

	written, err = s.Sync(ctx, first)
	require.NoError(t, err)
	assert.False(t, written)

	for _, a := range client.Actions() {
		assert.Equal(t, "get", a.GetVerb(), "no writes when unchanged")
	}

	client.ClearActions()

	second := &Rendered{Content: []byte("c2"), Secrets: map[string][]byte{"token.a": []byte(`"t2"`)}}

	written, err = s.Sync(ctx, second)
	require.NoError(t, err)
	assert.True(t, written)

	var writes []string

	for _, a := range client.Actions() {
		if a.GetVerb() == "update" || a.GetVerb() == "create" {
			writes = append(writes, a.GetVerb()+" "+a.GetResource().Resource)
		}
	}

	assert.Equal(t, []string{"update secrets", "update configmaps"}, writes, "Secret before ConfigMap")

	cm, err = client.CoreV1().ConfigMaps("ns").Get(ctx, "cm", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "c2", cm.Data[ConfigFileName])
}

func TestKubernetesSyncer_SecretFailureSkipsConfigMap(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("create", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, assert.AnError
	})

	_, err := NewKubernetesSyncer(client, "ns", "cm", "sec").Sync(context.Background(), &Rendered{Content: []byte("c")})
	require.Error(t, err)

	for _, a := range client.Actions() {
		assert.NotEqual(t, "configmaps", a.GetResource().Resource)
	}
}

func TestSyncers_RetryMarker(t *testing.T) {
	ctx := context.Background()
	base := &Rendered{Content: []byte("# Managed\n# content-hash: 1\nsinks: {}\n")}

	file := NewFileSyncer(t.TempDir())
	client := fake.NewSimpleClientset()
	syncers := map[string]Syncer{"file": file, "kubernetes": NewKubernetesSyncer(client, "ns", "cm", "sec")}

	for name, s := range syncers {
		t.Run(name, func(t *testing.T) {
			written, err := s.Sync(ctx, base)
			require.NoError(t, err)
			assert.True(t, written)

			// A retry rewrites the same config, every attempt.
			for _, marker := range []string{"a", "b"} {
				written, err = s.Sync(ctx, base.WithRetry(marker))
				require.NoError(t, err)
				assert.True(t, written, marker)
			}

			written, err = s.Sync(ctx, base.WithRetry("b"))
			require.NoError(t, err)
			assert.False(t, written, "the same attempt is not written twice")

			// The unmarked config (e.g. after a restart) does not rewrite a marked copy.
			written, err = s.Sync(ctx, base)
			require.NoError(t, err)
			assert.False(t, written)
		})
	}

	got, err := os.ReadFile(filepath.Join(file.dir, ConfigFileName))
	require.NoError(t, err)
	assert.Equal(t, string(base.WithRetry("b").Content), string(got))
}
