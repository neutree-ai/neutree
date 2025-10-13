package manifestapply

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/neutree-ai/neutree/internal/util"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Mutate is a callback function to modify objects before applying
type Mutate func(obj *unstructured.Unstructured) error

// ManifestApply handles Kubernetes manifest lifecycle operations
// It focuses on comparing, applying, and deleting manifests
type ManifestApply struct {
	ctrlClient client.Client
	namespace  string

	// Optional configurations that can be set once and reused
	lastAppliedConfigJSON string
	newObjects            *unstructured.UnstructuredList
	mutate                Mutate
	logger                klog.Logger
}

// NewManifestApply creates a new manifest apply manager
func NewManifestApply(ctrlClient client.Client, namespace string) *ManifestApply {
	return &ManifestApply{
		ctrlClient: ctrlClient,
		namespace:  namespace,
	}
}

// WithLastAppliedConfig sets the last applied configuration JSON
func (m *ManifestApply) WithLastAppliedConfig(configJSON string) *ManifestApply {
	m.lastAppliedConfigJSON = configJSON
	return m
}

// WithNewObjects sets the new desired state objects
func (m *ManifestApply) WithNewObjects(objects *unstructured.UnstructuredList) *ManifestApply {
	m.newObjects = objects
	return m
}

// WithOwnershipSetter sets the ownership setter callback
func (m *ManifestApply) WithMutate(mutate Mutate) *ManifestApply {
	m.mutate = mutate
	return m
}

// WithLogger sets the logger for the manifest apply operations
func (m *ManifestApply) WithLogger(logger klog.Logger) *ManifestApply {
	m.logger = logger
	return m
}

// ManifestDiff represents the difference between old and new manifests
type ManifestDiff struct {
	NeedsUpdate    bool                        // Whether any update is needed
	ChangedObjects []unstructured.Unstructured // Objects that need to be created or updated
	DeletedObjects []unstructured.Unstructured // Objects that need to be deleted
}

// computeManifestDiff compares new objects with last applied configuration
// Uses lastAppliedConfigJSON and newObjects from the manager if not provided
// lastAppliedConfigJSON: Optional override for last applied config (uses manager's if empty)
// newObjects: Optional override for new objects (uses manager's if nil)
// Returns the manifest diff including objects to update and delete
func (m *ManifestApply) computeManifestDiff() (*ManifestDiff, error) {
	lastAppliedConfigJSON := m.lastAppliedConfigJSON
	newObjects := m.newObjects

	if newObjects == nil {
		return nil, errors.New("newObjects is required (either as parameter or via WithNewObjects)")
	}

	diff := &ManifestDiff{
		ChangedObjects: []unstructured.Unstructured{},
		DeletedObjects: []unstructured.Unstructured{},
	}

	// If there's no last applied config, this is a first-time deployment
	if lastAppliedConfigJSON == "" {
		m.logger.V(4).Info("First deployment detected, all objects will be applied")

		diff.NeedsUpdate = true
		diff.ChangedObjects = newObjects.Items

		return diff, nil
	}

	// Parse the last applied configuration (stored as Items array)
	var lastObjects []unstructured.Unstructured
	if err := json.Unmarshal([]byte(lastAppliedConfigJSON), &lastObjects); err != nil {
		return nil, errors.Wrap(err, "failed to parse last applied config")
	}

	// Build a map of last objects for quick lookup
	lastObjectsMap := make(map[string]*unstructured.Unstructured)

	for i := range lastObjects {
		obj := &lastObjects[i]
		key := objectKey(obj)
		lastObjectsMap[key] = obj
	}

	// Compare new objects with last objects to find changed/new objects
	for _, newObj := range newObjects.Items {
		key := objectKey(&newObj)
		lastObj, exists := lastObjectsMap[key]

		if !exists {
			// New object that didn't exist before
			klog.V(4).InfoS("New object detected",
				"kind", newObj.GetKind(),
				"name", newObj.GetName())

			diff.ChangedObjects = append(diff.ChangedObjects, newObj)

			continue
		}

		// Compare specs using hash for efficiency
		newHash := computeSpecHash(&newObj)
		lastHash := computeSpecHash(lastObj)

		if newHash != lastHash {
			klog.V(4).InfoS("Object spec changed",
				"kind", newObj.GetKind(),
				"name", newObj.GetName(),
				"newHash", newHash,
				"lastHash", lastHash)

			diff.ChangedObjects = append(diff.ChangedObjects, newObj)
		}
	}

	// Find deleted objects (in last config but not in new config)
	newObjectsMap := make(map[string]bool)

	for _, newObj := range newObjects.Items {
		key := objectKey(&newObj)
		newObjectsMap[key] = true
	}

	for key, lastObj := range lastObjectsMap {
		if !newObjectsMap[key] {
			m.logger.V(4).Info("Object removed from configuration",
				"kind", lastObj.GetKind(),
				"name", lastObj.GetName())

			diff.DeletedObjects = append(diff.DeletedObjects, *lastObj)
		}
	}

	diff.NeedsUpdate = len(diff.ChangedObjects) > 0 || len(diff.DeletedObjects) > 0

	if !diff.NeedsUpdate {
		m.logger.V(4).Info("No changes detected in configuration")
	} else {
		m.logger.V(4).Info("Manifest diff computed",
			"changedObjects", len(diff.ChangedObjects),
			"deletedObjects", len(diff.DeletedObjects))
	}

	return diff, nil
}

// ApplyManifests applies changed objects to the cluster
// Uses ownershipSetter from the manager if not provided
// objects: Objects to apply
// ownershipSetter: Optional override for ownership setter (uses manager's if nil)
func (m *ManifestApply) ApplyManifests(
	ctx context.Context) (int, error) {
	diff, err := m.computeManifestDiff()
	if err != nil {
		return 0, errors.Wrap(err, "failed to compute manifest diff")
	}

	if !diff.NeedsUpdate {
		m.logger.V(4).Info("No manifest changes to apply")
		return 0, nil
	}

	objects := diff.ChangedObjects

	for i := range objects {
		obj := objects[i].DeepCopy()

		// Set mutation if callback provided
		if m.mutate != nil {
			if err := m.mutate(obj); err != nil {
				return 0, errors.Wrapf(err, "failed to set mutation for %s/%s",
					obj.GetKind(), obj.GetName())
			}
		}

		if err := util.CreateOrPatch(ctx, obj, m.ctrlClient); err != nil {
			return 0, errors.Wrapf(err, "failed to apply object %s/%s",
				obj.GetKind(),
				obj.GetName())
		}

		m.logger.Info("Applied object",
			"kind", obj.GetKind(),
			"name", obj.GetName(),
			"namespace", obj.GetNamespace())
	}

	deleteObjects := diff.DeletedObjects

	for i := range deleteObjects {
		obj := &deleteObjects[i]
		m.logger.Info("Deleting resource",
			"kind", obj.GetKind(),
			"name", obj.GetName(),
			"namespace", m.namespace)

		if err := m.ctrlClient.Delete(ctx, obj); err != nil {
			if !apierrors.IsNotFound(err) {
				klog.ErrorS(err, "Failed to delete resource",
					"kind", obj.GetKind(),
					"name", obj.GetName())
				// Continue with other resources
			}
		}
	}

	return len(objects) + len(deleteObjects), nil
}

func (m *ManifestApply) Delete(
	ctx context.Context) (bool, error) {
	if m.lastAppliedConfigJSON == "" {
		m.logger.V(4).Info("No last applied configuration, skipping deletion")
		return true, nil
	}

	// Parse the last applied configuration (stored as Items array)
	var lastObjects []unstructured.Unstructured
	if err := json.Unmarshal([]byte(m.lastAppliedConfigJSON), &lastObjects); err != nil {
		return false, errors.Wrap(err, "failed to parse last applied config")
	}

	deleteFinished := true

	for i := range lastObjects {
		obj := &lastObjects[i]
		m.logger.Info("Deleting resource",
			"kind", obj.GetKind(),
			"name", obj.GetName(),
			"namespace", m.namespace)

		err := m.ctrlClient.Get(ctx, client.ObjectKey{
			Name:      obj.GetName(),
			Namespace: obj.GetNamespace(),
		}, obj)
		if err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}

			return deleteFinished, errors.Wrapf(err, "failed to get resource %s/%s",
				obj.GetKind(),
				obj.GetName())
		}

		deleteFinished = false

		if obj.GetDeletionTimestamp() != nil {
			// Already marked for deletion
			continue
		}

		if err := m.ctrlClient.Delete(ctx, obj); err != nil {
			if !apierrors.IsNotFound(err) {
				klog.ErrorS(err, "Failed to delete resource",
					"kind", obj.GetKind(),
					"name", obj.GetName())
				// Continue with other resources
			}
		}
	}

	return deleteFinished, nil
}

// objectKey generates a unique key for a Kubernetes object
func objectKey(obj *unstructured.Unstructured) string {
	return fmt.Sprintf("%s/%s/%s/%s",
		obj.GetAPIVersion(),
		obj.GetKind(),
		obj.GetNamespace(),
		obj.GetName())
}

// computeSpecHash computes a hash of the object's spec for comparison
func computeSpecHash(obj *unstructured.Unstructured) string {
	spec, found := obj.Object["spec"]
	if !found {
		// For objects without spec (like ConfigMap), hash the entire object
		spec = obj.Object
	}

	specJSON, err := json.Marshal(spec)
	if err != nil {
		return ""
	}

	hash := sha256.Sum256(specJSON)

	return fmt.Sprintf("%x", hash)
}
