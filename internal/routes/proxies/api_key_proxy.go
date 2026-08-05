package proxies

import (
	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/admission"
	"github.com/neutree-ai/neutree/pkg/storage"
)

var apiKeyAdmissionResource = admission.NewResource[v1.ApiKey](storage.API_KEY_TABLE)

func RegisterAPIKeyRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) error {
	proxyGroup := group.Group("/api_keys")
	proxyGroup.Use(middlewares...)

	patchRunner, err := admissionPatchRunner(deps, storage.API_KEY_TABLE, apiKeyAdmissionResource)
	if err != nil {
		return err
	}

	handler := CreateStructProxyHandler[v1.ApiKey](deps, storage.API_KEY_TABLE)

	proxyGroup.GET("", handler)
	proxyGroup.PATCH("", rejectAPIKeyForceDelete(), withAdmissionRunner(patchRunner, handler)...)

	return nil
}
