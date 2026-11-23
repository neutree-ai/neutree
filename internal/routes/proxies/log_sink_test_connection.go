package proxies

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/middleware"
	"github.com/neutree-ai/neutree/internal/observability/logsink"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// logSinkTestConnectionTimeout bounds the whole test; the probe makes two
// (VictoriaLogs) or three (S3) requests, each capped on its own.
const logSinkTestConnectionTimeout = 30 * time.Second

// logSinkTestConnectionRequest selects what to test:
//   - spec only: an unsaved spec (create form), needs log_sink:create;
//   - id only: the stored sink with its stored secrets, needs log_sink:read;
//   - id and spec: an edited spec whose blank secrets come from the stored
//     sink when the type (and VictoriaLogs auth type) is unchanged, needs
//     log_sink:update.
type logSinkTestConnectionRequest struct {
	ID   *int            `json:"id,omitempty"`
	Spec *v1.LogSinkSpec `json:"spec,omitempty"`
}

type logSinkTestConnectionResponse struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
	Time    string `json:"time"`
}

// handleLogSinkTestConnection probes a sink on demand. The result is returned
// only; status.last_connection_test belongs to the controller.
func handleLogSinkTestConnection(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req logSinkTestConnectionRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
			return
		}

		if req.ID == nil && req.Spec == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "id or spec is required"})
			return
		}

		permission := "log_sink:create"

		switch {
		case req.ID != nil && req.Spec != nil:
			permission = "log_sink:update"
		case req.ID != nil:
			permission = "log_sink:read"
		}

		// The permission depends on the body, so the middleware runs inline.
		middleware.RequirePermission(permission, middleware.PermissionDependencies{Storage: deps.Storage})(c)

		if c.IsAborted() {
			return
		}

		spec := req.Spec

		if req.ID != nil {
			stored, err := deps.Storage.GetLogSink(strconv.Itoa(*req.ID))
			if err != nil {
				if errors.Is(err, storage.ErrResourceNotFound) {
					c.JSON(http.StatusNotFound, gin.H{"error": "log sink not found"})
					return
				}

				klog.Errorf("Failed to get log sink %d: %v", *req.ID, err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get log sink"})

				return
			}

			if spec == nil {
				spec = stored.Spec
			} else {
				backfillLogSinkSecrets(spec, stored.Spec)
			}
		}

		if err := logsink.ValidateSpec(spec); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		ctx, cancel := context.WithTimeout(c.Request.Context(), logSinkTestConnectionTimeout)
		defer cancel()

		resp := logSinkTestConnectionResponse{OK: true}
		if err := logsink.TestConnection(ctx, &v1.LogSink{Spec: spec}); err != nil {
			resp.OK = false
			resp.Message = err.Error()
		}

		resp.Time = time.Now().UTC().Format(time.RFC3339)

		c.JSON(http.StatusOK, resp)
	}
}

// backfillLogSinkSecrets fills secrets the edit form left blank (reads mask
// them) from the stored spec, only while the type (and for VictoriaLogs the
// auth type) is unchanged.
func backfillLogSinkSecrets(spec, stored *v1.LogSinkSpec) {
	if stored == nil || spec.Type != stored.Type {
		return
	}

	if spec.Type == v1.LogSinkTypeS3 {
		backfillS3Keys(spec.S3, stored.S3)
		return
	}

	if spec.VictoriaLogs == nil || spec.VictoriaLogs.Auth == nil ||
		stored.VictoriaLogs == nil || stored.VictoriaLogs.Auth == nil {
		return
	}

	auth, old := spec.VictoriaLogs.Auth, stored.VictoriaLogs.Auth
	if auth.Type != old.Type {
		return
	}

	switch auth.Type {
	case v1.LogSinkAuthTypeBasic:
		if auth.Password == "" {
			auth.Password = old.Password
		}
	case v1.LogSinkAuthTypeBearer:
		if auth.Token == "" {
			auth.Token = old.Token
		}
	}
}

// backfillS3Keys fills each blank key separately, like a PATCH does.
func backfillS3Keys(spec, stored *v1.S3SinkSpec) {
	if spec == nil || stored == nil || stored.Auth == nil {
		return
	}

	if spec.Auth == nil {
		spec.Auth = &v1.S3SinkAuth{}
	}

	if spec.Auth.AccessKeyID == "" {
		spec.Auth.AccessKeyID = stored.Auth.AccessKeyID
	}

	if spec.Auth.SecretAccessKey == "" {
		spec.Auth.SecretAccessKey = stored.Auth.SecretAccessKey
	}
}
