package deploy

import (
	"context"
	"encoding/base64"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// LastAppliedConfigKey is the key used to store last applied configuration in ConfigMap
	LastAppliedConfigKey = "last-applied-config"

	// ManagedByLabel is the standard Kubernetes label for managed-by
	ManagedByLabel = "app.kubernetes.io/managed-by"

	// ManagedByValue is the value for managed-by label
	ManagedByValue = "neutree"
)

// ConfigStore manages component configuration storage using Kubernetes ConfigMaps
type ConfigStore struct {
	client client.Client
}

// NewConfigStore creates a new ConfigStore instance
func NewConfigStore(client client.Client) *ConfigStore {
	return &ConfigStore{client: client}
}

// buildConfigMapName constructs the ConfigMap name
// Format: neutree-{resourceName}-{componentName}-config
// Examples:
//   - neutree-cluster-prod-router-config
//   - neutree-endpoint-demo-deployment-config
func (s *ConfigStore) buildConfigMapName(resourceName, componentName string) string {
	return fmt.Sprintf("neutree-%s-%s-config", resourceName, componentName)
}

// Get retrieves the last applied configuration from ConfigMap
// Returns empty string if ConfigMap doesn't exist (first deployment)
func (s *ConfigStore) Get(ctx context.Context, namespace, resourceName, componentName string) (string, error) {
	cm := &corev1.ConfigMap{}
	key := client.ObjectKey{
		Namespace: namespace,
		Name:      s.buildConfigMapName(resourceName, componentName),
	}

	err := s.client.Get(ctx, key, cm)
	if err != nil {
		if client.IgnoreNotFound(err) == nil {
			// ConfigMap doesn't exist, return empty (first deployment)
			return "", nil
		}

		return "", err
	}

	rawConfig, ok := cm.Data[LastAppliedConfigKey]
	if !ok || rawConfig == "" {
		return "", nil
	}

	decodedConfig, err := base64.StdEncoding.DecodeString(rawConfig)
	if err != nil {
		return "", err
	}

	return string(decodedConfig), nil
}

// Set saves the configuration to ConfigMap (CreateOrUpdate)
func (s *ConfigStore) Set(ctx context.Context, namespace, resourceName, componentName, config string, labels map[string]string) error {
	configMapName := s.buildConfigMapName(resourceName, componentName)

	// Merge labels
	if labels == nil {
		labels = make(map[string]string)
	}

	labels[ManagedByLabel] = ManagedByValue
	labels["neutree.io/resource"] = resourceName
	labels["neutree.io/component"] = componentName

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      configMapName,
			Labels:    labels,
		},
		Data: map[string]string{
			LastAppliedConfigKey: base64.StdEncoding.EncodeToString([]byte(config)),
		},
	}

	// Try to get existing ConfigMap
	existingCM := &corev1.ConfigMap{}
	key := client.ObjectKey{Namespace: namespace, Name: configMapName}
	err := s.client.Get(ctx, key, existingCM)

	if err != nil {
		if client.IgnoreNotFound(err) == nil {
			// ConfigMap doesn't exist, create new one
			return s.client.Create(ctx, cm)
		}

		return err
	}

	// ConfigMap exists, update it
	existingCM.Data = cm.Data
	existingCM.Labels = cm.Labels

	return s.client.Update(ctx, existingCM)
}

// Delete removes the ConfigMap
func (s *ConfigStore) Delete(ctx context.Context, namespace, resourceName, componentName string) error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      s.buildConfigMapName(resourceName, componentName),
		},
	}

	err := s.client.Delete(ctx, cm)

	return client.IgnoreNotFound(err)
}
