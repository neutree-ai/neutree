package v1

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLogSinkSpecIsEnabled(t *testing.T) {
	enabled, disabled := true, false

	var nilSpec *LogSinkSpec
	assert.True(t, nilSpec.IsEnabled())
	assert.True(t, (&LogSinkSpec{}).IsEnabled())
	assert.True(t, (&LogSinkSpec{Enabled: &enabled}).IsEnabled())
	assert.False(t, (&LogSinkSpec{Enabled: &disabled}).IsEnabled())
}

func TestLogSinkBufferGetMaxSize(t *testing.T) {
	var nilBuffer *LogSinkBuffer
	assert.Equal(t, LogSinkBufferDefaultMaxSize, nilBuffer.GetMaxSize())
	assert.Equal(t, LogSinkBufferDefaultMaxSize, (&LogSinkBuffer{}).GetMaxSize())
	assert.Equal(t, int64(512<<20), (&LogSinkBuffer{MaxSize: 512 << 20}).GetMaxSize())
}

func TestLogSinkJSONRoundTrip(t *testing.T) {
	raw := `{
		"api_version": "v1",
		"kind": "LogSink",
		"metadata": {"name": "vl"},
		"spec": {
			"type": "victorialogs",
			"enabled": false,
			"victorialogs": {
				"url": "https://vl.example.com:9428",
				"insecure_skip_verify": true,
				"timeout_seconds": 10,
				"auth": {"type": "basic", "username": "u", "password": "p"},
				"extra_fields": {"cluster": "a"}
			},
			"buffer": {"max_size": 268435488}
		},
		"status": {
			"phase": "Running",
			"sent_events": 3,
			"last_connection_test": {"time": "2026-01-01T00:00:00Z", "ok": true}
		}
	}`

	var sink LogSink
	require.NoError(t, json.Unmarshal([]byte(raw), &sink))

	assert.Equal(t, "vl", sink.GetName())
	assert.Equal(t, LogSinkTypeVictoriaLogs, sink.Spec.Type)
	assert.False(t, sink.Spec.IsEnabled())
	assert.Equal(t, "p", sink.Spec.VictoriaLogs.Auth.Password)
	assert.Equal(t, map[string]string{"cluster": "a"}, sink.Spec.VictoriaLogs.ExtraFields)
	assert.Equal(t, LogSinkBufferMinMaxSize, sink.Spec.Buffer.GetMaxSize())
	assert.Equal(t, LogSinkPhaseRUNNING, sink.Status.Phase)
	assert.True(t, sink.Status.LastConnectionTest.OK)
}

func TestS3SinkSpecDefaults(t *testing.T) {
	f := false
	empty := &S3SinkSpec{}

	assert.Equal(t, LogSinkS3DefaultKeyPrefix, empty.GetKeyPrefix())
	assert.True(t, empty.UsePathStyle())
	assert.Equal(t, LogSinkS3CompressionGzip, empty.GetCompression())
	assert.Equal(t, LogSinkS3DefaultFlushIntervalSeconds, empty.GetFlushIntervalSeconds())

	set := &S3SinkSpec{KeyPrefix: "x/", ForcePathStyle: &f, Compression: LogSinkS3CompressionZstd, FlushIntervalSeconds: 10}

	assert.Equal(t, "x/", set.GetKeyPrefix())
	assert.False(t, set.UsePathStyle())
	assert.Equal(t, LogSinkS3CompressionZstd, set.GetCompression())
	assert.Equal(t, 10, set.GetFlushIntervalSeconds())
}

func TestLogSinkS3JSONRoundTrip(t *testing.T) {
	raw := `{
		"metadata": {"name": "archive"},
		"spec": {
			"type": "s3",
			"s3": {
				"endpoint": "http://minio:9000",
				"region": "us-east-1",
				"bucket": "traces",
				"key_prefix": "neutree/%Y/",
				"force_path_style": false,
				"compression": "zstd",
				"flush_interval_seconds": 60,
				"auth": {"access_key_id": "ak", "secret_access_key": "sk"}
			}
		}
	}`

	var sink LogSink
	require.NoError(t, json.Unmarshal([]byte(raw), &sink))

	s3 := sink.Spec.S3
	require.NotNil(t, s3)
	assert.Equal(t, LogSinkTypeS3, sink.Spec.Type)
	assert.Nil(t, sink.Spec.VictoriaLogs)
	assert.Equal(t, "traces", s3.Bucket)
	assert.False(t, s3.UsePathStyle())
	assert.Equal(t, 60, s3.GetFlushIntervalSeconds())
	assert.Equal(t, "sk", s3.Auth.SecretAccessKey)

	out, err := json.Marshal(sink.Spec)
	require.NoError(t, err)
	assert.NotContains(t, string(out), "victorialogs")
}
