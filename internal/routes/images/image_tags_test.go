package images

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	v1 "github.com/neutree-ai/neutree/api/v1"
	registryMocks "github.com/neutree-ai/neutree/internal/registry/mocks"
	storageMocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

// createTestContextWithParams drives a handler the way the router does: the
// workspace, registry and repository come from the path, not the query.
func createTestContextWithParams(params map[string]string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

	for k, v := range params {
		c.Params = append(c.Params, gin.Param{Key: k, Value: v})
	}

	return c, w
}

func imageTagsDeps(t *testing.T, url string) (*Dependencies, *registryMocks.MockImageService) {
	t.Helper()

	store := storageMocks.NewMockStorage(t)
	imageService := registryMocks.NewMockImageService(t)
	store.On("ListImageRegistry", mock.Anything).Return([]v1.ImageRegistry{{
		Spec: &v1.ImageRegistrySpec{URL: url, Repository: "neutree-ai"},
	}}, nil)

	return &Dependencies{Storage: store, ImageService: imageService}, imageService
}

func TestGetImageTags(t *testing.T) {
	t.Run("asks the registry for the repository under its own prefix", func(t *testing.T) {
		deps, imageService := imageTagsDeps(t, "https://registry.example.com")
		imageService.
			On("ListImageTags", "registry.example.com/neutree-ai/my-workload", mock.Anything, false).
			Return([]string{"v1", "v2"}, nil)

		ctx, recorder := createTestContextWithParams(map[string]string{
			"workspace":  "default",
			"registry":   "registry",
			"repository": "my-workload",
		})
		getImageTags(deps)(ctx)

		assert.Equal(t, http.StatusOK, recorder.Code)

		var body ImageTagsResponse
		assert.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
		assert.Equal(t, "my-workload", body.Repository)
		assert.Equal(t, []string{"v1", "v2"}, body.Tags)
		imageService.AssertExpectations(t)
	})

	t.Run("carries the registry's scheme through, as pulls do", func(t *testing.T) {
		deps, imageService := imageTagsDeps(t, "http://registry.example.com:5000")
		imageService.
			On("ListImageTags", "registry.example.com:5000/neutree-ai/x", mock.Anything, true).
			Return([]string{"v1"}, nil)

		ctx, recorder := createTestContextWithParams(map[string]string{
			"workspace": "default", "registry": "registry", "repository": "x",
		})
		getImageTags(deps)(ctx)

		assert.Equal(t, http.StatusOK, recorder.Code)
		imageService.AssertExpectations(t)
	})

	t.Run("tolerates a repository written with slashes around it", func(t *testing.T) {
		deps, imageService := imageTagsDeps(t, "https://registry.example.com")
		imageService.
			On("ListImageTags", "registry.example.com/neutree-ai/team/x", mock.Anything, false).
			Return([]string{"v1"}, nil)

		ctx, recorder := createTestContextWithParams(map[string]string{
			"workspace": "default", "registry": "registry", "repository": " /team/x/ ",
		})
		getImageTags(deps)(ctx)

		assert.Equal(t, http.StatusOK, recorder.Code)
		imageService.AssertExpectations(t)
	})

	t.Run("reports a refusing registry as an upstream answer, not a server fault", func(t *testing.T) {
		// A caller offering these as suggestions has to be able to fall back to
		// free text, which it cannot decide on a 500.
		deps, imageService := imageTagsDeps(t, "https://registry.example.com")
		imageService.
			On("ListImageTags", mock.Anything, mock.Anything, mock.Anything).
			Return([]string(nil), errors.New("UNAUTHORIZED"))

		ctx, recorder := createTestContextWithParams(map[string]string{
			"workspace": "default", "registry": "registry", "repository": "x",
		})
		getImageTags(deps)(ctx)

		assert.Equal(t, http.StatusBadGateway, recorder.Code)
	})

	t.Run("rejects a repository that is only separators", func(t *testing.T) {
		// Workspace and registry come from the path, so the router will not
		// route a request that lacks them. A repository that trims away to
		// nothing would otherwise be asked for as the registry prefix itself.
		for _, repository := range []string{"", " ", "/", " // "} {
			ctx, recorder := createTestContextWithParams(map[string]string{
				"workspace": "default", "registry": "registry", "repository": repository,
			})
			getImageTags(&Dependencies{})(ctx)

			assert.Equalf(t, http.StatusBadRequest, recorder.Code, "repository %q", repository)
		}
	})

	t.Run("reports an unknown registry as not found", func(t *testing.T) {
		store := storageMocks.NewMockStorage(t)
		store.On("ListImageRegistry", mock.Anything).Return([]v1.ImageRegistry{}, nil)

		ctx, recorder := createTestContextWithParams(map[string]string{
			"workspace": "default", "registry": "absent", "repository": "x",
		})
		getImageTags(&Dependencies{Storage: store})(ctx)

		assert.Equal(t, http.StatusNotFound, recorder.Code)
	})
}
