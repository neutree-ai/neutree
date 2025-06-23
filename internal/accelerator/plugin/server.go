package plugin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

type PluginServer interface {
	Start(ctx context.Context)
}

type pluginServer struct {
	listenAddr string
	handler    RegisterHandle
}

func NewPluginServer(listenAddr string, rh RegisterHandle) PluginServer {
	return &pluginServer{
		listenAddr: listenAddr,
		handler:    rh,
	}
}

func (s *pluginServer) register(c *gin.Context) {
	reqContent, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var req v1.RegisterRequest

	err = json.Unmarshal(reqContent, &req)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	acceleratorPlugin := newAcceleratorRestPlugin(req.ResourceName, req.Endpoint)
	s.handler(acceleratorPlugin)

	c.JSON(http.StatusOK, nil)
}

func (s *pluginServer) Start(_ context.Context) {
	r := gin.Default()
	r.POST(v1.RegisterPath, s.register)

	go func() {
		err := r.Run(s.listenAddr)
		if err != nil {
			panic(err)
		}
	}()
}
