package logsink

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Syncer delivers a rendered config to where Vector reads it.
type Syncer interface {
	// Sync reports whether anything was written.
	Sync(ctx context.Context, r *Rendered) (bool, error)
}

// FileSyncer writes into a directory that is Vector's config dir (bind mounted).
type FileSyncer struct {
	dir string
}

func NewFileSyncer(dir string) *FileSyncer {
	return &FileSyncer{dir: dir}
}

func (s *FileSyncer) Sync(_ context.Context, r *Rendered) (bool, error) {
	configPath := filepath.Join(s.dir, ConfigFileName)

	current, err := os.ReadFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		return false, errors.Wrapf(err, "failed to read %s", configPath)
	}

	// The content embeds a hash of the secrets, so equal content means nothing to do.
	if err == nil && UpToDate(current, r.Content) {
		return false, nil
	}

	secretsDir := filepath.Join(s.dir, SecretsDirName)
	if err := os.MkdirAll(secretsDir, 0o700); err != nil {
		return false, errors.Wrapf(err, "failed to create %s", secretsDir)
	}

	for name, data := range r.Secrets {
		path := filepath.Join(secretsDir, name)
		if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, data) {
			continue
		}

		if err := writeAtomic(path, filepath.Join(secretsDir, "."+name+".tmp"), data, 0o600); err != nil {
			return false, err
		}
	}

	// The temp name has no config extension, so Vector never loads a partial file.
	if err := writeAtomic(configPath, filepath.Join(s.dir, "."+ConfigFileName+".tmp"), r.Content, 0o644); err != nil {
		return false, err
	}

	// Stale secrets go last: removing one the running config still references
	// would make the reload it triggers fail.
	entries, err := os.ReadDir(secretsDir)
	if err != nil {
		return true, errors.Wrapf(err, "failed to list %s", secretsDir)
	}

	for _, e := range entries {
		if _, ok := r.Secrets[e.Name()]; ok || e.IsDir() {
			continue
		}

		if err := os.Remove(filepath.Join(secretsDir, e.Name())); err != nil && !os.IsNotExist(err) {
			return true, errors.Wrapf(err, "failed to remove stale secret %s", e.Name())
		}
	}

	return true, nil
}

func writeAtomic(path, tmp string, data []byte, perm os.FileMode) error {
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return errors.Wrapf(err, "failed to write %s", tmp)
	}

	// WriteFile keeps the mode of an existing file.
	if err := os.Chmod(tmp, perm); err != nil {
		return errors.Wrapf(err, "failed to chmod %s", tmp)
	}

	if err := os.Rename(tmp, path); err != nil {
		return errors.Wrapf(err, "failed to rename %s", tmp)
	}

	return nil
}

// KubernetesSyncer keeps a Secret and a ConfigMap that the Vector Deployment mounts.
type KubernetesSyncer struct {
	client     kubernetes.Interface
	namespace  string
	configMap  string
	secretName string
}

func NewKubernetesSyncer(client kubernetes.Interface, namespace, configMap, secretName string) *KubernetesSyncer {
	return &KubernetesSyncer{client: client, namespace: namespace, configMap: configMap, secretName: secretName}
}

var managedLabels = map[string]string{"app.kubernetes.io/managed-by": "neutree-core"}

func (s *KubernetesSyncer) Sync(ctx context.Context, r *Rendered) (bool, error) {
	// Secret first, so the config never references a secret Vector cannot see yet.
	secretChanged, err := s.syncSecret(ctx, r.Secrets)
	if err != nil {
		return false, err
	}

	cmChanged, err := s.syncConfigMap(ctx, r.Content)
	if err != nil {
		return secretChanged, err
	}

	return secretChanged || cmChanged, nil
}

func (s *KubernetesSyncer) syncSecret(ctx context.Context, data map[string][]byte) (bool, error) {
	if data == nil {
		data = map[string][]byte{}
	}

	secrets := s.client.CoreV1().Secrets(s.namespace)

	existing, err := secrets.Get(ctx, s.secretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = secrets.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: s.secretName, Namespace: s.namespace, Labels: managedLabels},
			Type:       corev1.SecretTypeOpaque,
			Data:       data,
		}, metav1.CreateOptions{})
		if err != nil {
			return false, errors.Wrapf(err, "failed to create secret %s", s.secretName)
		}

		return true, nil
	}

	if err != nil {
		return false, errors.Wrapf(err, "failed to get secret %s", s.secretName)
	}

	if len(existing.Data) == 0 && len(data) == 0 || reflect.DeepEqual(existing.Data, data) {
		return false, nil
	}

	existing.Data = data
	if _, err = secrets.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return false, errors.Wrapf(err, "failed to update secret %s", s.secretName)
	}

	return true, nil
}

func (s *KubernetesSyncer) syncConfigMap(ctx context.Context, content []byte) (bool, error) {
	data := map[string]string{ConfigFileName: string(content)}
	configMaps := s.client.CoreV1().ConfigMaps(s.namespace)

	existing, err := configMaps.Get(ctx, s.configMap, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = configMaps.Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: s.configMap, Namespace: s.namespace, Labels: managedLabels},
			Data:       data,
		}, metav1.CreateOptions{})
		if err != nil {
			return false, errors.Wrapf(err, "failed to create configmap %s", s.configMap)
		}

		return true, nil
	}

	if err != nil {
		return false, errors.Wrapf(err, "failed to get configmap %s", s.configMap)
	}

	if len(existing.Data) == 1 && UpToDate([]byte(existing.Data[ConfigFileName]), content) {
		return false, nil
	}

	existing.Data = data
	if _, err = configMaps.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return false, errors.Wrapf(err, "failed to update configmap %s", s.configMap)
	}

	return true, nil
}
