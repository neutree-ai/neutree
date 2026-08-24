package v1

import (
	"testing"

	"github.com/neutree-ai/neutree/pkg/scheme"
	"github.com/stretchr/testify/assert"
)

// Every accessor guards against a nil Metadata, because a project decoded from
// a partial payload has none and callers read these without checking.
func TestApiKeyProjectAccessorsTolerateNilMetadata(t *testing.T) {
	obj := &ApiKeyProject{}

	assert.Equal(t, "", obj.GetName())
	assert.Equal(t, "", obj.GetWorkspace())
	assert.Equal(t, "", obj.GetCreationTimestamp())
	assert.Equal(t, "", obj.GetUpdateTimestamp())
	assert.Equal(t, "", obj.GetDeletionTimestamp())
	assert.Nil(t, obj.GetLabels())
	assert.Nil(t, obj.GetAnnotations())
	assert.Nil(t, obj.GetStatus())
}

func TestApiKeyProjectAccessorsReadMetadata(t *testing.T) {
	obj := &ApiKeyProject{
		ID:   "project-id",
		Kind: "ApiKeyProject",
		Metadata: &Metadata{
			Name:              "Support",
			Workspace:         "default",
			CreationTimestamp: "2026-01-01T00:00:00Z",
			UpdateTimestamp:   "2026-01-02T00:00:00Z",
			DeletionTimestamp: "2026-01-03T00:00:00Z",
			Labels:            map[string]string{"team": "support"},
			Annotations:       map[string]string{"note": "shared"},
		},
		Spec: &ApiKeyProjectSpec{Description: "Shared folder"},
	}

	assert.Equal(t, "Support", obj.GetName())
	assert.Equal(t, "default", obj.GetWorkspace())
	assert.Equal(t, "2026-01-01T00:00:00Z", obj.GetCreationTimestamp())
	assert.Equal(t, "2026-01-02T00:00:00Z", obj.GetUpdateTimestamp())
	assert.Equal(t, "2026-01-03T00:00:00Z", obj.GetDeletionTimestamp())
	assert.Equal(t, map[string]string{"team": "support"}, obj.GetLabels())
	assert.Equal(t, map[string]string{"note": "shared"}, obj.GetAnnotations())
	assert.Equal(t, "project-id", obj.GetID())
	assert.Equal(t, "ApiKeyProject", obj.GetKind())
	assert.Equal(t, obj.Spec, obj.GetSpec())
	assert.Equal(t, obj.Metadata, obj.GetMetadata())

	obj.SetKind("Other")
	assert.Equal(t, "Other", obj.GetKind())
}

// The setters allocate Metadata rather than panicking on a nil one.
func TestApiKeyProjectSettersAllocateMetadata(t *testing.T) {
	labelled := &ApiKeyProject{}
	labelled.SetLabels(map[string]string{"a": "b"})
	assert.Equal(t, map[string]string{"a": "b"}, labelled.GetLabels())

	annotated := &ApiKeyProject{}
	annotated.SetAnnotations(map[string]string{"c": "d"})
	assert.Equal(t, map[string]string{"c": "d"}, annotated.GetAnnotations())
}

// SetItems copies by value, so the list must not alias the objects handed to it.
func TestApiKeyProjectListItemsRoundTrip(t *testing.T) {
	list := &ApiKeyProjectList{}
	list.SetKind("ApiKeyProjectList")
	assert.Equal(t, "ApiKeyProjectList", list.GetKind())

	first := &ApiKeyProject{ID: "one", Metadata: &Metadata{Name: "One"}}
	second := &ApiKeyProject{ID: "two", Metadata: &Metadata{Name: "Two"}}
	list.SetItems([]scheme.Object{first, second})

	items := list.GetItems()
	assert.Len(t, items, 2)
	assert.Equal(t, "one", items[0].GetID())
	assert.Equal(t, "two", items[1].GetID())

	// Mutating the source must not reach into the list's copy.
	first.ID = "mutated"
	assert.Equal(t, "one", list.GetItems()[0].GetID())
}
