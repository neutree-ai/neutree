package logs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	v1 "github.com/neutree-ai/neutree/api/v1"
	utilmocks "github.com/neutree-ai/neutree/internal/util/mocks"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

func newK8sLogsTestRouter(test *testing.T) (*gin.Engine, *utilmocks.MockK8sClient) {
	test.Helper()
	gin.SetMode(gin.TestMode)
	store := new(storagemocks.MockStorage)
	endpoint := v1.Endpoint{
		Metadata: &v1.Metadata{Workspace: "default", Name: "test-endpoint"},
		Spec:     &v1.EndpointSpec{Cluster: "test-cluster"},
	}
	cluster := v1.Cluster{
		Metadata: &v1.Metadata{Workspace: "default", Name: "test-cluster"},
		Spec:     &v1.ClusterSpec{Type: v1.KubernetesClusterType},
	}
	store.On("ListEndpoint", mock.Anything).Return([]v1.Endpoint{endpoint}, nil).Once()
	store.On("ListCluster", mock.Anything).Return([]v1.Cluster{cluster}, nil).Once()
	store.On("CallDatabaseFunction", "has_permission", map[string]interface{}{
		"user_uuid": "log-reader", "required_permission": "endpoint:read", "workspace": "default",
	}, mock.Anything).Run(func(arguments mock.Arguments) {
		permission, ok := arguments.Get(2).(*bool)
		require.True(test, ok)
		*permission = true
	}).Return(nil).Once()
	test.Cleanup(func() { store.AssertExpectations(test) })
	kubernetesClient := utilmocks.NewMockK8sClient(test)
	router := gin.New()
	identity := func(ginContext *gin.Context) { ginContext.Set("user_id", "log-reader") }
	RegisterEndpointLogsRoutes(router.Group("/api/v1"), []gin.HandlerFunc{identity}, &Dependencies{Storage: store, K8sClient: kubernetesClient})

	return router, kubernetesClient
}

func endpointLogTestPod() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "pod-123",
			Labels: map[string]string{"workspace": "default", "endpoint": "test-endpoint", "app": "inference"},
		},
		Spec: corev1.PodSpec{
			Containers:     []corev1.Container{{Name: "serve"}, {Name: "sidecar"}},
			InitContainers: []corev1.Container{{Name: "model-downloader"}},
		},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "sidecar"}, {Name: "serve", RestartCount: 1},
			},
			InitContainerStatuses: []corev1.ContainerStatus{{
				Name: "model-downloader", RestartCount: 1,
				State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}},
			}},
		},
	}
}

func TestHandleK8sLogs_ContainerSelection(test *testing.T) {
	tests := []struct {
		name      string
		query     string
		method    string
		container string
		previous  bool
		lines     int64
		download  bool
	}{
		{name: "default main current", container: "serve", lines: 1000},
		{name: "sidecar current", query: "container=sidecar&previous=false", container: "sidecar", lines: 1000},
		{name: "main previous", query: "container=serve&previous=true", container: "serve", previous: true, lines: 1000},
		{name: "completed init current", query: "container=model-downloader", container: "model-downloader", lines: 1000},
		{
			name: "completed init previous", query: "container=model-downloader&previous=true&lines=25",
			container: "model-downloader", previous: true, lines: 25,
		},
		{
			name: "download selected current", query: "container=sidecar&download=true", container: "sidecar",
			lines: 1000, download: true,
		},
		{
			name: "download selected previous", query: "container=model-downloader&previous=true&download=true",
			container: "model-downloader", previous: true, lines: 1000, download: true,
		},
		{
			name: "head validates selection", method: http.MethodHead,
			query: "container=model-downloader&previous=true&download=true", container: "model-downloader",
			previous: true, download: true,
		},
	}
	for _, testCase := range tests {
		test.Run(testCase.name, func(test *testing.T) {
			router, kubernetesClient := newK8sLogsTestRouter(test)
			kubernetesClient.On("GetPod", mock.Anything, mock.Anything, mock.Anything, "pod-123").Return(endpointLogTestPod(), nil).Once()
			content := fmt.Sprintf("container=%s previous=%t\n", testCase.container, testCase.previous)
			if testCase.method != http.MethodHead {
				kubernetesClient.On("GetPodLogs", mock.Anything, mock.Anything, mock.Anything, "pod-123",
					mock.MatchedBy(func(options *corev1.PodLogOptions) bool {
						return options.Container == testCase.container && options.Previous == testCase.previous &&
							options.Timestamps && options.TailLines != nil && *options.TailLines == testCase.lines && !options.Follow
					})).Return(io.NopCloser(strings.NewReader(content)), nil).Once()
			}
			method := testCase.method
			if method == "" {
				method = http.MethodGet
			}
			request := httptest.NewRequest(method, "/api/v1/endpoints/default/test-endpoint/logs/pod-123/logs?"+testCase.query, nil)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)

			require.Equal(test, http.StatusOK, recorder.Code, recorder.Body.String())
			assert.Equal(test, "text/plain; charset=utf-8", recorder.Header().Get("Content-Type"))
			if method == http.MethodHead {
				assert.Empty(test, recorder.Body.String())
			} else {
				assert.Equal(test, content, recorder.Body.String())
			}
			if testCase.download {
				mode := "current"
				if testCase.previous {
					mode = "previous"
				}
				assert.Contains(test, recorder.Header().Get("Content-Disposition"), "pod-123-"+testCase.container+"-"+mode+".log")
			} else {
				assert.Empty(test, recorder.Header().Get("Content-Disposition"))
			}
		})
	}
}

func TestHandleK8sLogs_InvalidSource(test *testing.T) {
	tests := []struct {
		name       string
		query      string
		logType    string
		pod        *corev1.Pod
		statusCode int
		errorText  string
	}{
		{name: "unknown container", query: "container=missing", statusCode: http.StatusBadRequest, errorText: "container"},
		{name: "unknown init container on head", query: "container=missing&download=true", statusCode: http.StatusBadRequest, errorText: "container"},
		{name: "invalid previous", query: "previous=invalid", statusCode: http.StatusBadRequest, errorText: "previous"},
		{name: "wrong log type", logType: "stdout", statusCode: http.StatusBadRequest, errorText: "log type"},
		{
			name: "other endpoint", pod: &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{"workspace": "default", "endpoint": "other", "app": "inference"},
			}}, statusCode: http.StatusNotFound, errorText: "endpoint",
		},
		{
			name: "other workspace", pod: &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{"workspace": "other", "endpoint": "test-endpoint", "app": "inference"},
			}}, statusCode: http.StatusNotFound, errorText: "endpoint",
		},
		{
			name: "not an inference pod", pod: &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{"workspace": "default", "endpoint": "test-endpoint", "app": "system"},
			}}, statusCode: http.StatusNotFound, errorText: "endpoint",
		},
		{name: "missing ownership labels", pod: &corev1.Pod{}, statusCode: http.StatusNotFound, errorText: "endpoint"},
	}
	for _, testCase := range tests {
		test.Run(testCase.name, func(test *testing.T) {
			router, kubernetesClient := newK8sLogsTestRouter(test)
			pod := testCase.pod
			if pod == nil {
				pod = endpointLogTestPod()
			}
			if testCase.logType == "" && testCase.name != "invalid previous" {
				kubernetesClient.On("GetPod", mock.Anything, mock.Anything, mock.Anything, "pod-123").Return(pod, nil).Once()
			}
			logType := testCase.logType
			if logType == "" {
				logType = "logs"
			}
			method := http.MethodGet
			if testCase.name == "unknown init container on head" {
				method = http.MethodHead
			}
			request := httptest.NewRequest(method, "/api/v1/endpoints/default/test-endpoint/logs/pod-123/"+logType+"?"+testCase.query, nil)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)

			assert.Equal(test, testCase.statusCode, recorder.Code, recorder.Body.String())
			assert.Contains(test, recorder.Body.String(), testCase.errorText)
			assert.Contains(test, recorder.Header().Get("Content-Type"), "application/json")
			assert.Empty(test, recorder.Header().Get("Content-Disposition"))
		})
	}
}

func TestHandleK8sLogs_Unavailable(test *testing.T) {
	tests := []struct {
		name       string
		query      string
		podError   error
		logsError  error
		statusCode int
		errorText  string
	}{
		{
			name: "deleted pod", podError: apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, "pod-123"),
			statusCode: http.StatusNotFound, errorText: "pod",
		},
		{name: "pod API failure", podError: errors.New("private upstream details"), statusCode: http.StatusInternalServerError, errorText: "pod"},
		{
			name: "previous not retained", query: "container=serve&previous=true&download=true",
			logsError:  apierrors.NewBadRequest("previous terminated container not found"),
			statusCode: http.StatusBadRequest, errorText: "previous",
		},
		{name: "current upstream failure", logsError: errors.New("private upstream details"), statusCode: http.StatusInternalServerError, errorText: "logs"},
	}
	for _, testCase := range tests {
		test.Run(testCase.name, func(test *testing.T) {
			router, kubernetesClient := newK8sLogsTestRouter(test)
			kubernetesClient.On("GetPod", mock.Anything, mock.Anything, mock.Anything, "pod-123").Return(endpointLogTestPod(), testCase.podError).Once()
			if testCase.podError == nil {
				kubernetesClient.On("GetPodLogs", mock.Anything, mock.Anything, mock.Anything, "pod-123", mock.Anything).
					Return(nil, testCase.logsError).Once()
			}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/api/v1/endpoints/default/test-endpoint/logs/pod-123/logs?"+testCase.query, nil)
			router.ServeHTTP(recorder, request)

			assert.Equal(test, testCase.statusCode, recorder.Code, recorder.Body.String())
			assert.Contains(test, recorder.Body.String(), testCase.errorText)
			assert.NotContains(test, recorder.Body.String(), "private upstream details")
			assert.Contains(test, recorder.Header().Get("Content-Type"), "application/json")
			assert.Empty(test, recorder.Header().Get("Content-Disposition"))
		})
	}
}

func TestK8sLogErrorStatus(test *testing.T) {
	pods := schema.GroupResource{Resource: "pods"}
	cases := []struct {
		name       string
		err        error
		statusCode int
	}{
		{name: "bad request", err: apierrors.NewBadRequest("invalid log options"), statusCode: http.StatusBadRequest},
		{name: "not found", err: apierrors.NewNotFound(pods, "pod-123"), statusCode: http.StatusNotFound},
		{name: "generic error", err: errors.New("upstream failure"), statusCode: http.StatusInternalServerError},
		{name: "deadline exceeded", err: context.DeadlineExceeded, statusCode: http.StatusInternalServerError},
		{name: "canceled", err: context.Canceled, statusCode: http.StatusInternalServerError},
		{name: "forbidden", err: apierrors.NewForbidden(pods, "pod-123", errors.New("denied")), statusCode: http.StatusInternalServerError},
		{name: "unauthorized", err: apierrors.NewUnauthorized("unauthorized"), statusCode: http.StatusInternalServerError},
		{name: "timeout", err: apierrors.NewTimeoutError("request timed out", 1), statusCode: http.StatusInternalServerError},
		{name: "server error", err: apierrors.NewInternalError(errors.New("server failure")), statusCode: http.StatusInternalServerError},
		{
			name: "wrapped bad request", err: fmt.Errorf("get logs: %w", apierrors.NewBadRequest("invalid log options")),
			statusCode: http.StatusBadRequest,
		},
		{
			name: "wrapped not found", err: fmt.Errorf("get pod: %w", apierrors.NewNotFound(pods, "pod-123")),
			statusCode: http.StatusNotFound,
		},
		{name: "wrapped deadline", err: fmt.Errorf("get logs: %w", context.DeadlineExceeded), statusCode: http.StatusInternalServerError},
	}
	for _, testCase := range cases {
		test.Run(testCase.name, func(test *testing.T) {
			assert.Equal(test, testCase.statusCode, k8sLogErrorStatus(testCase.err))
		})
	}
}
