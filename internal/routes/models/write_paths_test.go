package models

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/model_registry"
)

// countingReader records how much of a request body was consumed, so that
// "refused before the upload" is asserted rather than assumed.
type countingReader struct {
	reader io.Reader
	read   int
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.read += n

	return n, err
}

// modelUploadBody builds a push the way the CLI sends one: metadata parts first,
// then the model payload.
func modelUploadBody(t *testing.T) (*countingReader, string) {
	t.Helper()

	var buf bytes.Buffer

	writer := multipart.NewWriter(&buf)
	require.NoError(t, writer.WriteField("name", "qwen3"))
	require.NoError(t, writer.WriteField("version", "v1"))
	require.NoError(t, writer.WriteField("model_size", "1048576"))

	part, err := writer.CreateFormFile("model", "model.bentomodel")
	require.NoError(t, err)
	_, err = part.Write(bytes.Repeat([]byte("x"), 1<<20))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	return &countingReader{reader: &buf}, writer.FormDataContentType()
}

func newUploadContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder, *countingReader) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	body, contentType := modelUploadBody(t)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost,
		"/api/v1/workspaces/default/model_registries/test-registry/models", body)
	c.Request.Header.Set("Content-Type", contentType)
	c.Params = []gin.Param{
		{Key: "workspace", Value: "default"},
		{Key: "registry", Value: "test-registry"},
	}

	return c, w, body
}

func registryRowOfType(registryType v1.ModelRegistryType) []v1.ModelRegistry {
	return []v1.ModelRegistry{{
		ID:       7,
		Metadata: &v1.Metadata{Name: "test-registry", Workspace: "default"},
		Spec:     &v1.ModelRegistrySpec{Type: registryType, Url: "https://huggingface.co"},
	}}
}

// A push to a public registry must be refused with a status code, which is only
// possible while nothing has been committed to the response.
func TestUploadModel_PublicRegistryRefusedBeforeTheBodyIsRead(t *testing.T) {
	mockStorage, mockRegistry := setupMocks(t)
	mockStorage.On("ListModelRegistry", mock.Anything).
		Return(registryRowOfType(v1.HuggingFaceModelRegistryType), nil)

	c, w, body := newUploadContext(t)
	uploadModel(&Dependencies{Storage: mockStorage})(c)

	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), reasonNotSupported)

	// No chunked framing and a JSON body, i.e. the response was not already open.
	assert.Empty(t, w.Header().Get("Transfer-Encoding"))
	assert.Contains(t, w.Header().Get("Content-Type"), "application/json")

	// The point of the whole change.
	assert.Zero(t, body.read, "the request body must not be read before the registry is refused")
	mockRegistry.AssertNotCalled(t, "ImportModel", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	mockRegistry.AssertNotCalled(t, "Connect")
}

// The guard keys off the registry, so the private path has to be shown still
// importing — otherwise "refuses public registries" could pass by refusing all.
func TestUploadModel_PrivateRegistryStillUploads(t *testing.T) {
	mockStorage, mockRegistry := setupMocks(t)
	mockStorage.On("ListModelRegistry", mock.Anything).
		Return(registryRowOfType(v1.BentoMLModelRegistryType), nil)
	mockRegistry.On("Connect").Return(nil)
	mockRegistry.On("Disconnect").Return(nil)

	imported := false

	mockRegistry.On("ImportModel", mock.Anything, "qwen3", "v1", mock.Anything).
		Run(func(args mock.Arguments) {
			// Drain the payload the way the real import does, so the body is genuinely
			// consumed on this path.
			reader, _ := args.Get(0).(io.Reader)
			if reader != nil {
				_, _ = io.Copy(io.Discard, reader)
			}

			imported = true
		}).Return(nil)

	c, w, body := newUploadContext(t)
	uploadModel(&Dependencies{Storage: mockStorage})(c)

	require.Equal(t, http.StatusOK, w.Code)
	assert.True(t, imported, "a private registry must still receive the upload")
	assert.Contains(t, w.Body.String(), "Success: Model imported successfully")
	assert.Positive(t, body.read, "the private path does read the body")
}

// Deleting from a public registry is a refusal, not a server fault. The private
// path is covered by TestDeleteModel_Success.
func TestDeleteModel_PublicRegistryRefusalIsABadRequest(t *testing.T) {
	mockStorage, mockRegistry := setupMocks(t)
	mockStorage.On("ListModelRegistry", mock.Anything).
		Return(registryRowOfType(v1.HuggingFaceModelRegistryType), nil)
	mockStorage.On("ListEndpoint", mock.Anything).Return([]v1.Endpoint{}, nil)
	mockStorage.On("ListModelCatalog", mock.Anything).Return([]v1.ModelCatalog{}, nil).Maybe()
	mockRegistry.On("Connect").Return(nil)
	mockRegistry.On("Disconnect").Return(nil)
	mockRegistry.On("GetModelVersion", mock.Anything, mock.Anything).
		Return(nil, errors.Wrap(model_registry.ErrNotSupported, "hugging face")).Maybe()
	mockRegistry.On("DeleteModel", "qwen3", v1.LatestVersion).
		Return(errors.Wrap(model_registry.ErrNotSupported, "hugging face"))

	c, w := createMockContext("default", "test-registry", "qwen3", "")
	deleteModel(&Dependencies{Storage: mockStorage})(c)

	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), reasonNotSupported)
}

// Finalizing is a write and is refused the same way, before the registry is asked
// anything.
func TestFinalizeModel_PublicRegistryIsRefused(t *testing.T) {
	mockStorage, mockRegistry := setupMocks(t)
	mockStorage.On("ListModelRegistry", mock.Anything).
		Return(registryRowOfType(v1.HuggingFaceModelRegistryType), nil)
	mockRegistry.On("Connect").Return(nil)
	mockRegistry.On("Disconnect").Return(nil)

	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost,
		"/api/v1/workspaces/default/model_registries/test-registry/models/qwen3/finalize",
		bytes.NewBufferString(`{"version":"v1"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = []gin.Param{
		{Key: "workspace", Value: "default"},
		{Key: "registry", Value: "test-registry"},
		{Key: "model", Value: "qwen3"},
	}

	finalizeModel(&Dependencies{Storage: mockStorage})(c)

	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), reasonNotSupported)
	mockRegistry.AssertNotCalled(t, "GetModelVersion", mock.Anything, mock.Anything)
}
