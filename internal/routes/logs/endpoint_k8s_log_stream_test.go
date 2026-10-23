package logs

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type trackedK8sLogStream struct {
	io.Reader
	closed bool
}

func (stream *trackedK8sLogStream) Close() error {
	stream.closed = true

	return nil
}

func TestHandleK8sLogs_StreamReadFailure(test *testing.T) {
	cases := []struct {
		name       string
		reader     io.Reader
		statusCode int
		content    string
	}{
		{name: "first read failed", reader: iotest.ErrReader(io.ErrUnexpectedEOF), statusCode: http.StatusInternalServerError},
		{
			name: "failed after content", reader: io.MultiReader(strings.NewReader("partial logs\n"), iotest.ErrReader(io.ErrUnexpectedEOF)),
			statusCode: http.StatusOK, content: "partial logs\n",
		},
	}
	for _, testCase := range cases {
		test.Run(testCase.name, func(test *testing.T) {
			router, kubernetesClient := newK8sLogsTestRouter(test)
			kubernetesClient.On("GetPod", mock.Anything, mock.Anything, mock.Anything, "pod-123").Return(endpointLogTestPod(), nil).Once()
			stream := &trackedK8sLogStream{Reader: testCase.reader}
			kubernetesClient.On("GetPodLogs", mock.Anything, mock.Anything, mock.Anything, "pod-123", mock.Anything).Return(stream, nil).Once()
			request := httptest.NewRequest(http.MethodGet,
				"/api/v1/endpoints/default/test-endpoint/logs/pod-123/logs?container=serve&previous=true&download=true", nil)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)

			assert.Equal(test, testCase.statusCode, recorder.Code)
			assert.True(test, stream.closed)
			if testCase.content != "" {
				assert.Equal(test, testCase.content, recorder.Body.String())

				return
			}

			assert.Contains(test, recorder.Header().Get("Content-Type"), "application/json")
			assert.Contains(test, recorder.Body.String(), "previous logs")
			assert.Empty(test, recorder.Header().Get("Content-Disposition"))
			assert.Empty(test, recorder.Header().Get("Transfer-Encoding"))
		})
	}
}

func TestHandleK8sLogs_TimeoutAndCancellation(test *testing.T) {
	cases := []struct {
		name       string
		method     string
		stage      string
		cancel     bool
		statusCode int
	}{
		{name: "HEAD pod timeout", method: http.MethodHead, stage: "pod", statusCode: http.StatusInternalServerError},
		{name: "GET log timeout", method: http.MethodGet, stage: "logs", statusCode: http.StatusInternalServerError},
		{name: "HEAD cancellation", method: http.MethodHead, stage: "pod", cancel: true, statusCode: http.StatusInternalServerError},
		{name: "GET cancellation", method: http.MethodGet, stage: "logs", cancel: true, statusCode: http.StatusInternalServerError},
	}
	for _, testCase := range cases {
		test.Run(testCase.name, func(test *testing.T) {
			router, kubernetesClient := newK8sLogsTestRouter(test)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
			defer cancel()
			requestError := context.DeadlineExceeded
			if testCase.cancel {
				cancel()
				requestError = context.Canceled
			}

			waitForContext := func(arguments mock.Arguments) {
				requestContext, ok := arguments.Get(0).(context.Context)
				require.True(test, ok)
				<-requestContext.Done()
			}
			podExpectation := kubernetesClient.On("GetPod", mock.Anything, mock.Anything, mock.Anything, "pod-123")
			if testCase.stage == "pod" {
				podExpectation.Run(waitForContext).Return(nil, requestError).Once()
			} else {
				podExpectation.Return(endpointLogTestPod(), nil).Once()
				kubernetesClient.On("GetPodLogs", mock.Anything, mock.Anything, mock.Anything, "pod-123", mock.Anything).
					Run(waitForContext).Return(nil, requestError).Once()
			}

			request := httptest.NewRequest(testCase.method, "/api/v1/endpoints/default/test-endpoint/logs/pod-123/logs", nil).WithContext(ctx)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)

			assert.Equal(test, testCase.statusCode, recorder.Code)
			assert.Contains(test, recorder.Header().Get("Content-Type"), "application/json")
		})
	}
}

func TestHandleK8sLogs_UpstreamDeadline(test *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		test.Run(method, func(test *testing.T) {
			router, kubernetesClient := newK8sLogsTestRouter(test)
			var upstreamContext context.Context
			kubernetesClient.On("GetPod", mock.Anything, mock.Anything, mock.Anything, "pod-123").
				Run(func(arguments mock.Arguments) {
					var ok bool
					upstreamContext, ok = arguments.Get(0).(context.Context)
					require.True(test, ok)
					deadline, exists := upstreamContext.Deadline()
					assert.True(test, exists, "Kubernetes requests must have a bounded lifetime")
					assert.WithinDuration(test, time.Now().Add(30*time.Second), deadline, time.Second)
				}).Return(endpointLogTestPod(), nil).Once()
			if method == http.MethodGet {
				kubernetesClient.On("GetPodLogs", mock.Anything, mock.Anything, mock.Anything, "pod-123", mock.Anything).
					Run(func(arguments mock.Arguments) {
						assert.Same(test, upstreamContext, arguments.Get(0))
						assert.NoError(test, upstreamContext.Err())
					}).Return(io.NopCloser(strings.NewReader("current logs\n")), nil).Once()
			}
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(method, "/api/v1/endpoints/default/test-endpoint/logs/pod-123/logs", nil))

			assert.Equal(test, http.StatusOK, recorder.Code)
			require.NotNil(test, upstreamContext)
			assert.ErrorIs(test, upstreamContext.Err(), context.Canceled)
		})
	}
}

func TestStreamK8sLogs_SuccessClosesStream(test *testing.T) {
	router, mockClient := newK8sLogsTestRouter(test)
	mockClient.On("GetPod", mock.Anything, mock.Anything, mock.Anything, "pod-123").Return(endpointLogTestPod(), nil).Once()
	stream := &trackedK8sLogStream{Reader: strings.NewReader("current logs\n")}
	mockClient.On("GetPodLogs", mock.Anything, mock.Anything, mock.Anything, "pod-123", mock.Anything).Return(stream, nil).Once()
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/endpoints/default/test-endpoint/logs/pod-123/logs", nil))

	assert.Equal(test, http.StatusOK, recorder.Code)
	assert.Equal(test, "current logs\n", recorder.Body.String())
	assert.True(test, stream.closed)
}
