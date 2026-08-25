package images

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/go-containerregistry/pkg/authn"

	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// ImageTagsResponse lists the tags one repository holds in an image registry.
type ImageTagsResponse struct {
	// Repository echoed as the caller named it, so a client rendering several
	// answers can tell them apart without tracking its own requests.
	Repository string   `json:"repository"`
	Tags       []string `json:"tags"`
}

// getImageTags handles
// GET /workspaces/:workspace/image_registries/:registry/repositories/:repository/tags.
//
// It answers "which tags does this repository have?", not "which repositories
// does this registry have?". Enumerating repositories needs the registry's
// catalog endpoint, which Docker Hub does not implement at all and which
// self-hosted registries commonly restrict to a wider scope than pulling -- a
// pull-scoped credential, the only kind a working deployment is guaranteed to
// hold, is refused there. Tags come back with the same scope pulls use, so this
// answer is available wherever deploying already works.
//
// The repository is named relative to the registry's own repository prefix.
func getImageTags(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		workspace := c.Param("workspace")
		imageRegistryName := c.Param("registry")

		repository := strings.Trim(strings.TrimSpace(c.Param("repository")), "/")
		if repository == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "repository is required"})
			return
		}

		imageRegistries, err := deps.Storage.ListImageRegistry(storage.ListOption{
			Filters: []storage.Filter{
				{Column: "metadata->name", Operator: "eq", Value: fmt.Sprintf(`"%s"`, imageRegistryName)},
				{Column: "metadata->workspace", Operator: "eq", Value: fmt.Sprintf(`"%s"`, workspace)},
			},
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to get image registry: %v", err)})
			return
		}

		if len(imageRegistries) == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("image registry %s/%s not found", workspace, imageRegistryName)})
			return
		}

		imageRegistry := &imageRegistries[0]

		imagePrefix, err := util.GetImagePrefix(imageRegistry)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to get image prefix: %v", err)})
			return
		}

		username, password, err := util.GetImageRegistryAuthInfo(imageRegistry)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to get auth info: %v", err)})
			return
		}

		var auth authn.Authenticator = authn.Anonymous
		if username != "" || password != "" {
			auth = authn.FromConfig(authn.AuthConfig{Username: username, Password: password})
		}

		tags, err := deps.ImageService.ListImageTags(
			imagePrefix+"/"+repository,
			auth,
			util.IsHTTPRegistryURL(imageRegistry.Spec.URL),
		)
		if err != nil {
			// The registry refusing or not knowing the repository is an answer
			// about the request, not a fault in the control plane: a caller
			// offering these as suggestions has to be able to fall back to free
			// text rather than show a server error.
			c.JSON(http.StatusBadGateway, gin.H{
				"error": fmt.Sprintf("failed to list tags for %s: %v", repository, err),
			})

			return
		}

		c.JSON(http.StatusOK, ImageTagsResponse{Repository: repository, Tags: tags})
	}
}
