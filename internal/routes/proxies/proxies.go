package proxies

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/sashabaranov/go-openai"
	"k8s.io/klog/v2"

	"github.com/neutree-ai/neutree/pkg/storage"
)

type Dependencies struct {
	Storage storage.Storage

	StorageAccessURL string
	ServiceToken     string

	AuthEndpoint string
}

func CreateProxyHandler(targetURL string, path string, modifyRequest func(*http.Request)) gin.HandlerFunc {
	target, err := url.Parse(fmt.Sprintf("%s/%s", targetURL, path))
	if err != nil {
		klog.Errorf("Failed to parse target URL: %v", err)

		return func(c *gin.Context) {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "Failed to create proxy",
			})
		}
	}

	proxy := httputil.NewSingleHostReverseProxy(target)

	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.URL.Path = target.Path
		req.Host = target.Host

		if modifyRequest != nil {
			modifyRequest(req)
		}
	}

	return func(c *gin.Context) {
		proxy.ServeHTTP(c.Writer, c.Request)
	}
}

func RegisterRoutes(r *gin.Engine, deps *Dependencies) {
	r.Any("/api/v1/serve-proxy/:name/*path", handleServeProxy(deps))
	r.Any("/api/v1/ray-dashboard-proxy/:name/*path", handleRayDashboardProxy(deps))
	r.Any("/api/v1/auth/:path", handleAuthProxy(deps))
	r.Any("/api/v1/:path", handlePostgrestProxy(deps))
	r.Any("/api/v1/playgrounds/chat/:endpoint", handlePlaygroundProxy(deps))
}

func handleServeProxy(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		name := c.Param("name")
		if name == "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "name is required",
			})

			return
		}

		endpoints, err := deps.Storage.ListEndpoint(storage.ListOption{
			Filters: []storage.Filter{
				{
					Column:   "metadata->name",
					Operator: "eq",
					Value:    strconv.Quote(name),
				},
			},
		})
		if err != nil {
			errS := fmt.Sprintf("Failed to list endpoints: %v", err)
			klog.Errorf(errS)

			c.JSON(http.StatusBadRequest, gin.H{
				"error": errS,
			})

			return
		}

		if len(endpoints) == 0 {
			c.JSON(http.StatusNotFound, gin.H{
				"error": "endpoint not found",
			})

			return
		}

		serviceURL := endpoints[0].Status.ServiceURL
		if serviceURL == "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "service_url not found",
			})

			return
		}

		path := c.Param("path")
		if path != "" && path[0] == '/' {
			path = path[1:]
		}

		// TODO: fix this in engine
		if c.Request.Method != "GET" && c.Request.Method != "HEAD" {
			bodyBytes, err := io.ReadAll(c.Request.Body)
			c.Request.Body.Close()

			if err == nil && len(bodyBytes) > 0 {
				var requestBody map[string]interface{}
				if err := json.Unmarshal(bodyBytes, &requestBody); err == nil {
					if _, exists := requestBody["encoding_format"]; exists {
						delete(requestBody, "encoding_format")

						modifiedBodyBytes, err := json.Marshal(requestBody)
						if err == nil {
							c.Request.Body = io.NopCloser(strings.NewReader(string(modifiedBodyBytes)))
							c.Request.ContentLength = int64(len(modifiedBodyBytes))
							c.Request.Header.Set("Content-Length", fmt.Sprintf("%d", len(modifiedBodyBytes)))
						}
					} else {
						c.Request.Body = io.NopCloser(strings.NewReader(string(bodyBytes)))
					}
				} else {
					c.Request.Body = io.NopCloser(strings.NewReader(string(bodyBytes)))
				}
			}
		}

		proxyHandler := CreateProxyHandler(serviceURL, path, nil)
		proxyHandler(c)
	}
}

func handleRayDashboardProxy(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		name := c.Param("name")
		if name == "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "name is required",
			})

			return
		}

		clusters, err := deps.Storage.ListCluster(storage.ListOption{
			Filters: []storage.Filter{
				{
					Column:   "metadata->name",
					Operator: "eq",
					Value:    strconv.Quote(name),
				},
			},
		})

		if err != nil {
			errS := fmt.Sprintf("Failed to list clusters: %v", err)
			klog.Errorf(errS)

			c.JSON(http.StatusBadRequest, gin.H{
				"error": errS,
			})

			return
		}

		if len(clusters) == 0 {
			c.JSON(http.StatusNotFound, gin.H{
				"error": "cluster not found",
			})

			return
		}

		dashboardURL := clusters[0].Status.DashboardURL
		if dashboardURL == "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "dashboard_url not found",
			})

			return
		}

		path := c.Param("path")
		if path != "" && path[0] == '/' {
			path = path[1:]
		}

		proxyHandler := CreateProxyHandler(dashboardURL, path, nil)
		proxyHandler(c)
	}
}

func handleAuthProxy(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Param("path")
		if path != "" && path[0] == '/' {
			path = path[1:]
		}

		proxyHandler := CreateProxyHandler(deps.AuthEndpoint, path, nil)
		proxyHandler(c)
	}
}

func handlePostgrestProxy(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Param("path")
		if path != "" && path[0] == '/' {
			path = path[1:]
		}

		proxyHandler := CreateProxyHandler(deps.StorageAccessURL, path, func(req *http.Request) {
			req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", deps.ServiceToken))
		})
		proxyHandler(c)
	}
}

type ChatRequest struct {
	Model       string                         `json:"model"`
	Messages    []openai.ChatCompletionMessage `json:"messages"`
	ID          string                         `json:"id"`
	Temperature float32                        `json:"temperature,omitempty"`
	TopP        float32                        `json:"top_p,omitempty"`
	MaxLength   int                            `json:"max_length,omitempty"`
}

func handlePlaygroundProxy(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		endpoint := c.Param("endpoint")
		if endpoint == "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "endpoint is required",
			})

			return
		}

		endpoints, err := deps.Storage.ListEndpoint(storage.ListOption{
			Filters: []storage.Filter{
				{
					Column:   "metadata->name",
					Operator: "eq",
					Value:    strconv.Quote(endpoint),
				},
			},
		})

		if err != nil {
			errS := fmt.Sprintf("Failed to list endpoints: %v", err)
			klog.Error(errS)
			c.JSON(http.StatusBadRequest, gin.H{
				"error": errS,
			})

			return
		}

		if len(endpoints) == 0 {
			c.JSON(http.StatusNotFound, gin.H{
				"error": "endpoint not found",
			})
		}

		serviceURL := endpoints[0].Status.ServiceURL
		if serviceURL == "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "service_url not found",
			})

			return
		}

		clientConfig := openai.DefaultConfig("")
		clientConfig.BaseURL = serviceURL + "/v1"
		client := openai.NewClientWithConfig(clientConfig)

		requestContent, err := io.ReadAll(c.Request.Body)
		if err != nil {
			errS := fmt.Sprintf("Failed to read request body: %v", err)
			klog.Error(errS)
			c.JSON(http.StatusBadRequest, gin.H{
				"error": errS,
			})

			return
		}

		var chatRequest ChatRequest
		if err = json.Unmarshal(requestContent, &chatRequest); err != nil {
			errS := fmt.Sprintf("Failed to unmarshal request body: %v", err)
			klog.Error(errS)
			c.JSON(http.StatusBadRequest, gin.H{
				"error": errS,
			})

			return
		}

		chatCompletionParams := openai.ChatCompletionRequest{
			Model:       chatRequest.Model,
			Temperature: chatRequest.Temperature,
			TopP:        chatRequest.TopP,
			MaxTokens:   chatRequest.MaxLength,
			Messages: []openai.ChatCompletionMessage{
				{
					Role:    openai.ChatMessageRoleSystem,
					Content: "You are a helpful assistant.",
				},
			},
		}

		for _, message := range chatRequest.Messages {
			chatCompletionParams.Messages = append(chatCompletionParams.Messages, openai.ChatCompletionMessage{
				Role:    message.Role,
				Content: message.Content,
			})
		}

		ctx := context.Background()

		stream, err := client.CreateChatCompletionStream(ctx, chatCompletionParams)
		if err != nil {
			errS := fmt.Sprintf("Failed to create chat completion stream: %v", err)
			klog.Error(errS)
			c.JSON(http.StatusBadRequest, gin.H{
				"error": errS,
			})

			return
		}

		defer stream.Close()

		w := c.Writer
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)

		for {
			response, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				break
			}

			if err != nil {
				w.Write([]byte(fmt.Sprintf("Stream error: %v", err))) // nolint: errcheck
				return
			}

			w.Write([]byte(response.Choices[0].Delta.Content)) // nolint: errcheck
			w.(http.Flusher).Flush()                           // nolint: errcheck
		}
	}
}
