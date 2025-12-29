package middleware

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"k8s.io/klog/v2"

	"github.com/neutree-ai/neutree/internal/utils/request"
)

type DeletionError struct {
	Code         string
	Message      string
	Hint         string
	ResourceType string
	ResourceName string
}

func (e *DeletionError) Error() string {
	return fmt.Sprintf("%s: %s (hint: %s)", e.Code, e.Message, e.Hint)
}

type DeletionValidatorFunc func(workspace, name string) error

func DeletionValidation(tableName string, validatorFunc DeletionValidatorFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method != http.MethodPatch {
			c.Next()
			return
		}

		bodyCtx, err := request.ExtractBody(c)
		if err != nil {
			c.Next()
			return
		}

		request.RestoreBody(c, bodyCtx.BodyBytes)

		if !request.IsSoftDeleteRequest(bodyCtx.BodyMap) {
			c.Next()
			return
		}

		workspace, name, err := request.ExtractResourceIdentifiers(bodyCtx.BodyMap)
		if err != nil {
			klog.Infof("Could not extract resource identifiers: %v, skipping validation", err)
			c.Next()

			return
		}

		klog.Infof("Validating deletion for %s: workspace=%s, name=%s", tableName, workspace, name)

		if err := validatorFunc(workspace, name); err != nil {
			handleValidationError(c, err)
			return
		}

		c.Next()
	}
}

func handleValidationError(c *gin.Context, err error) {
	if deletionErr, ok := err.(*DeletionError); ok {
		response := map[string]interface{}{
			"code":    deletionErr.Code,
			"message": deletionErr.Message,
			"hint":    deletionErr.Hint,
		}

		c.Header("X-Powered-By", "Neutree")
		c.JSON(http.StatusBadRequest, response)
		c.Abort()

		return
	}

	c.JSON(http.StatusInternalServerError, gin.H{
		"code":    "500",
		"message": fmt.Sprintf("Failed to validate deletion: %v", err),
	})
	c.Abort()
}
