package proxies

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

func builtinRegistryRow(builtin bool) v1.ModelRegistry {
	annotations := map[string]string{}
	if builtin {
		annotations = v1.WithBuiltinAnnotation(nil)
	}

	return v1.ModelRegistry{
		ID: 4,
		Metadata: &v1.Metadata{
			Name:        "public-hugging-face",
			Workspace:   "default",
			Annotations: annotations,
		},
		Spec: &v1.ModelRegistrySpec{
			Type:        v1.HuggingFaceModelRegistryType,
			Url:         "https://huggingface.co",
			Credentials: "hf_stored",
		},
	}
}

// runGuard drives the middleware over one PATCH body and reports whether the
// request was allowed through.
func runGuard(t *testing.T, storage *storagemocks.MockStorage, body map[string]interface{},
	userID string) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	raw, err := json.Marshal(body)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPatch, "/api/v1/model_registries", bytes.NewReader(raw))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user_id", userID)

	passed := false

	builtinModelRegistryGuard(&Dependencies{Storage: storage})(c)

	// gin's Next() is a no-op on a context built this way, so "was it allowed
	// through" is read off the abort flag rather than by chaining a handler.
	if !c.IsAborted() {
		passed = true
	}

	return w, passed
}

func patchBody(spec map[string]interface{}, deletionTimestamp string) map[string]interface{} {
	metadata := map[string]interface{}{"workspace": "default", "name": "public-hugging-face"}
	if deletionTimestamp != "" {
		metadata["deletion_timestamp"] = deletionTimestamp
	}

	body := map[string]interface{}{"metadata": metadata}
	if spec != nil {
		body["spec"] = spec
	}

	return body
}

// The control plane provisions it back on the next reconcile, so allowing the
// delete would make it flicker rather than remove it.
func TestBuiltinModelRegistryGuard_RefusesDeletion(t *testing.T) {
	storage := &storagemocks.MockStorage{}
	storage.On("ListModelRegistry", mock.Anything).Return([]v1.ModelRegistry{builtinRegistryRow(true)}, nil)

	w, passed := runGuard(t, storage, patchBody(nil, "2026-08-05T00:00:00Z"), "user-1")

	assert.False(t, passed)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), builtinRegistryImmutableCode)
}

// A different type under a name that says otherwise, undone by the next
// reconcile.
func TestBuiltinModelRegistryGuard_RefusesTypeChange(t *testing.T) {
	storage := &storagemocks.MockStorage{}
	storage.On("ListModelRegistry", mock.Anything).Return([]v1.ModelRegistry{builtinRegistryRow(true)}, nil)

	w, passed := runGuard(t, storage, patchBody(map[string]interface{}{
		"type": string(v1.BentoMLModelRegistryType),
	}, ""), "user-1")

	assert.False(t, passed)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "type cannot be changed")
}

// The address is owned by the deployment configuration, so editing it through the
// API is refused outright rather than accepted and reconciled away.
func TestBuiltinModelRegistryGuard_RefusesAddressChange(t *testing.T) {
	storage := &storagemocks.MockStorage{}
	storage.On("ListModelRegistry", mock.Anything).Return([]v1.ModelRegistry{builtinRegistryRow(true)}, nil)

	w, passed := runGuard(t, storage, patchBody(map[string]interface{}{
		"url": "https://hf-mirror.example",
	}, ""), "admin")

	assert.False(t, passed)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), builtinRegistryImmutableCode)
	assert.Contains(t, w.Body.String(), "hugging-face-endpoint")
	// Refused for everyone, so it is not a permission question and none is asked.
	storage.AssertNotCalled(t, "CallDatabaseFunction", mock.Anything, mock.Anything, mock.Anything)
}

// Credentials are the user's, so they stay editable — by someone trusted with
// them.
func TestBuiltinModelRegistryGuard_CredentialsNeedThePermission(t *testing.T) {
	tests := []struct {
		name string
		spec map[string]interface{}
	}{
		{name: "credentials", spec: map[string]interface{}{"credentials": "hf_new"}},
	}

	for _, tt := range tests {
		t.Run(tt.name+" refused without it", func(t *testing.T) {
			storage := &storagemocks.MockStorage{}
			storage.On("ListModelRegistry", mock.Anything).
				Return([]v1.ModelRegistry{builtinRegistryRow(true)}, nil)
			storage.On("CallDatabaseFunction", "has_permission", mock.Anything, mock.Anything).
				Run(func(args mock.Arguments) {
					result, ok := args.Get(2).(*bool)
					if ok {
						*result = false
					}
				}).Return(nil)

			w, passed := runGuard(t, storage, patchBody(tt.spec, ""), "user-1")

			assert.False(t, passed)
			assert.Equal(t, http.StatusForbidden, w.Code)
			assert.Contains(t, w.Body.String(), credentialsPermission)
		})

		t.Run(tt.name+" allowed with it", func(t *testing.T) {
			storage := &storagemocks.MockStorage{}
			storage.On("ListModelRegistry", mock.Anything).
				Return([]v1.ModelRegistry{builtinRegistryRow(true)}, nil)
			storage.On("CallDatabaseFunction", "has_permission", mock.Anything, mock.Anything).
				Run(func(args mock.Arguments) {
					result, ok := args.Get(2).(*bool)
					if ok {
						*result = true
					}
				}).Return(nil)

			_, passed := runGuard(t, storage, patchBody(tt.spec, ""), "admin")

			assert.True(t, passed)
		})
	}
}

// A patch that mentions the fields without changing them is not a change, and a
// patch that touches something else entirely was never this guard's business.
func TestBuiltinModelRegistryGuard_LetsUnchangedAndUnrelatedEditsThrough(t *testing.T) {
	tests := []struct {
		name string
		body map[string]interface{}
	}{
		{
			name: "same values resent",
			body: patchBody(map[string]interface{}{
				"url":         "https://huggingface.co",
				"credentials": "hf_stored",
				"type":        string(v1.HuggingFaceModelRegistryType),
			}, ""),
		},
		{
			name: "labels only",
			body: map[string]interface{}{
				"metadata": map[string]interface{}{
					"workspace": "default",
					"name":      "public-hugging-face",
					"labels":    map[string]interface{}{"team": "platform"},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			storage := &storagemocks.MockStorage{}
			storage.On("ListModelRegistry", mock.Anything).
				Return([]v1.ModelRegistry{builtinRegistryRow(true)}, nil)

			_, passed := runGuard(t, storage, tt.body, "user-1")

			assert.True(t, passed)
			storage.AssertNotCalled(t, "CallDatabaseFunction", mock.Anything, mock.Anything, mock.Anything)
		})
	}
}

// None of this applies to a registry a user created, whatever it is called.
func TestBuiltinModelRegistryGuard_IgnoresUserRegistries(t *testing.T) {
	storage := &storagemocks.MockStorage{}
	storage.On("ListModelRegistry", mock.Anything).Return([]v1.ModelRegistry{builtinRegistryRow(false)}, nil)

	_, passed := runGuard(t, storage, patchBody(map[string]interface{}{
		"type": string(v1.BentoMLModelRegistryType),
		"url":  "nfs://server/models",
	}, "2026-08-05T00:00:00Z"), "user-1")

	assert.True(t, passed)
}

// The marker is an identity: neither adding it to a user's registry nor removing
// it from a built-in one may be done through the API.
func TestBuiltinModelRegistryGuard_RefusesAnnotationChanges(t *testing.T) {
	tests := []struct {
		name    string
		builtin bool
		set     map[string]interface{}
	}{
		{
			name:    "adding it to a user registry",
			builtin: false,
			set:     map[string]interface{}{v1.BuiltinAnnotationKey: v1.BuiltinAnnotationValue},
		},
		{
			name:    "removing it from a built-in registry",
			builtin: true,
			set:     map[string]interface{}{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			storage := &storagemocks.MockStorage{}
			storage.On("ListModelRegistry", mock.Anything).
				Return([]v1.ModelRegistry{builtinRegistryRow(tt.builtin)}, nil)

			body := patchBody(nil, "")
			metadata, _ := body["metadata"].(map[string]interface{})
			metadata["annotations"] = tt.set

			w, passed := runGuard(t, storage, body, "admin")

			assert.False(t, passed)
			assert.Equal(t, http.StatusBadRequest, w.Code)
			assert.Contains(t, w.Body.String(), v1.BuiltinAnnotationKey)
		})
	}
}

// A client that read the object and sends it back unchanged must not be refused:
// PostgREST replaces the whole metadata composite, so the marker travels with
// every such write.
func TestBuiltinModelRegistryGuard_AllowsResendingTheAnnotationUnchanged(t *testing.T) {
	storage := &storagemocks.MockStorage{}
	storage.On("ListModelRegistry", mock.Anything).Return([]v1.ModelRegistry{builtinRegistryRow(true)}, nil)

	body := patchBody(nil, "")
	metadata, _ := body["metadata"].(map[string]interface{})
	metadata["annotations"] = map[string]interface{}{
		v1.BuiltinAnnotationKey: v1.BuiltinAnnotationValue,
		"team":                  "platform",
	}

	_, passed := runGuard(t, storage, body, "user-1")

	assert.True(t, passed)
}
