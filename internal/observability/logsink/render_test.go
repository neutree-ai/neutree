package logsink

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func testSink(name string, mutate func(*v1.VictoriaLogsSinkSpec)) *v1.LogSink {
	s := &v1.LogSink{
		Metadata: &v1.Metadata{Name: name},
		Spec: &v1.LogSinkSpec{
			Type:         v1.LogSinkTypeVictoriaLogs,
			VictoriaLogs: &v1.VictoriaLogsSinkSpec{URL: "https://vl.example.com:9428/"},
		},
		Status: &v1.LogSinkStatus{ComponentID: "logsink_" + strings.ReplaceAll(name, ".", "_")},
	}
	if mutate != nil {
		mutate(s.Spec.VictoriaLogs)
	}

	return s
}

func TestComponentIDAndSecretKey(t *testing.T) {
	id := NewComponentID("a-b.c")
	assert.Regexp(t, `^logsink_a-b_c_[0-9a-f]{8}$`, id)
	assert.True(t, ValidComponentID(id))
	// Every activation gets its own ID, so its disk buffer dir is never one an old sink still holds.
	assert.NotEqual(t, id, NewComponentID("a-b.c"))

	for _, bad := range []string{"", "logsink_", "logsink_a.b", "other_a", "logsink_A"} {
		assert.False(t, ValidComponentID(bad), bad)
	}

	assert.Equal(t, "token.a_b.c", secretKey("token", "a-b.c"))
	// '.' and '-' map to different characters, so distinct names stay distinct.
	assert.NotEqual(t, secretKey("token", "a.b"), secretKey("token", "a-b"))
}

func TestRender_RequiresComponentID(t *testing.T) {
	s := testSink("a", nil)
	s.Status = nil
	_, err := Render([]*v1.LogSink{s})
	require.Error(t, err)

	s.Status = &v1.LogSinkStatus{ComponentID: "logsink_a.b"}
	_, err = Render([]*v1.LogSink{s})
	require.Error(t, err)
}

func TestRendered_WithRetry(t *testing.T) {
	r, err := Render([]*v1.LogSink{testSink("a", nil)})
	require.NoError(t, err)

	retry := r.WithRetry("1")
	assert.NotEqual(t, string(r.Content), string(retry.Content))
	assert.Equal(t, string(r.Content), string(stripRetry(retry.Content)))

	var doc map[string]interface{}
	require.NoError(t, yaml.Unmarshal(retry.Content, &doc))
	assert.Contains(t, doc["sinks"], "logsink_a")

	// A marked config is up to date only if it is exactly what is there.
	assert.True(t, UpToDate(retry.Content, retry.Content))
	assert.False(t, UpToDate(r.WithRetry("0").Content, retry.Content))
	assert.False(t, UpToDate(r.Content, retry.Content))
	// Without a marker, a marked copy of the same config needs no rewrite.
	assert.True(t, UpToDate(retry.Content, r.Content))
	assert.True(t, UpToDate(r.Content, r.Content))

	other, err := Render([]*v1.LogSink{testSink("b", nil)})
	require.NoError(t, err)
	assert.False(t, UpToDate(retry.Content, other.Content))
}

func TestRender_Sinks(t *testing.T) {
	basic := testSink("prod-vl", func(vl *v1.VictoriaLogsSinkSpec) {
		vl.Auth = &v1.LogSinkAuth{Type: v1.LogSinkAuthTypeBasic, Username: "u$er", Password: `p@ss: "x" #y`}
		vl.CACert = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"
		vl.TimeoutSeconds = 15
		vl.ExtraFields = map[string]string{"zone": "eu 1", "cluster": "a&b"}
	})
	bearer := testSink("audit.vl", func(vl *v1.VictoriaLogsSinkSpec) {
		vl.Auth = &v1.LogSinkAuth{Type: v1.LogSinkAuthTypeBearer, Token: "tok"}
		vl.InsecureSkipVerify = true
	})
	bearer.Spec.Buffer = &v1.LogSinkBuffer{MaxSize: 536870912}

	r, err := Render([]*v1.LogSink{basic, bearer})
	require.NoError(t, err)

	content := string(r.Content)
	assert.True(t, strings.HasPrefix(content, "# Managed by neutree-core"))
	assert.Contains(t, content, "password: SECRET[logsink.password.prod_vl]\n", "secret refs must stay plain scalars")
	assert.Contains(t, content, "token: SECRET[logsink.token.audit.vl]\n")

	var doc struct {
		Secret map[string]map[string]string    `yaml:"secret"`
		Sinks  map[string]map[string]yaml.Node `yaml:"sinks"`
	}
	require.NoError(t, yaml.Unmarshal(r.Content, &doc))
	assert.Equal(t, map[string]string{"type": "directory", "path": "/etc/vector/logsink-secrets"}, doc.Secret["logsink"])
	require.Contains(t, doc.Sinks, "logsink_prod-vl")
	require.Contains(t, doc.Sinks, "logsink_audit_vl")

	var generic map[string]map[string]map[string]interface{}
	require.NoError(t, yaml.Unmarshal(r.Content, &generic))

	b := generic["sinks"]["logsink_prod-vl"]
	assert.Equal(t, []interface{}{"prepare_trace_payload"}, b["inputs"])
	assert.Equal(t, "https://vl.example.com:9428/insert/jsonline?_stream_fields=workspace,endpoint_type,endpoint_name"+
		"&_time_field=timestamp&extra_fields=cluster%3Da%26b%2Czone%3Deu+1", b["uri"])
	assert.Equal(t, map[string]interface{}{
		"strategy": "basic", "user": "u$$er", "password": "SECRET[logsink.password.prod_vl]",
	}, b["auth"])
	assert.Equal(t, map[string]interface{}{"ca_file": "/etc/vector/logsink-secrets/ca.prod_vl"}, b["tls"])
	assert.Equal(t, map[string]interface{}{
		"headers": map[string]interface{}{"Content-Type": "application/stream+json"}, "timeout_secs": 15,
	}, b["request"])
	assert.Equal(t, map[string]interface{}{"type": "disk", "max_size": 1073741824 + 134217728, "when_full": "drop_newest"}, b["buffer"])
	assert.Equal(t, map[string]interface{}{"max_events": 100, "timeout_secs": 1}, b["batch"])

	a := generic["sinks"]["logsink_audit_vl"]
	assert.Equal(t, map[string]interface{}{"strategy": "bearer", "token": "SECRET[logsink.token.audit.vl]"}, a["auth"])
	assert.Equal(t, map[string]interface{}{"verify_certificate": false, "verify_hostname": false}, a["tls"])
	assert.Equal(t, map[string]interface{}{"headers": map[string]interface{}{"Content-Type": "application/stream+json"}}, a["request"])
	// Vector stops accepting at max_size minus one 128MiB data file, so that file is added on top.
	assert.Equal(t, 536870912+134217728, a["buffer"].(map[string]interface{})["max_size"])

	// Secrets are YAML double-quoted scalars, since Vector pastes them into the text.
	assert.Equal(t, map[string][]byte{
		"password.prod_vl": []byte(`"p@ss: \"x\" #y"`),
		"ca.prod_vl":       []byte(basic.Spec.VictoriaLogs.CACert),
		"token.audit.vl":   []byte(`"tok"`),
	}, r.Secrets)

	var decoded string
	require.NoError(t, yaml.Unmarshal(r.Secrets["password.prod_vl"], &decoded))
	assert.Equal(t, `p@ss: "x" #y`, decoded)
}

func TestRender_Deterministic(t *testing.T) {
	a := testSink("a", func(vl *v1.VictoriaLogsSinkSpec) {
		vl.ExtraFields = map[string]string{"k1": "v1", "k2": "v2", "k3": "v3"}
	})
	b := testSink("b", nil)

	first, err := Render([]*v1.LogSink{a, b})
	require.NoError(t, err)

	for i := 0; i < 20; i++ {
		again, err := Render([]*v1.LogSink{b, a})
		require.NoError(t, err)
		assert.Equal(t, string(first.Content), string(again.Content))
	}

	assert.Less(t, strings.Index(string(first.Content), "logsink_a:"), strings.Index(string(first.Content), "logsink_b:"))
}

func TestRender_SecretChangeChangesContent(t *testing.T) {
	mk := func(token string) *Rendered {
		r, err := Render([]*v1.LogSink{testSink("a", func(vl *v1.VictoriaLogsSinkSpec) {
			vl.Auth = &v1.LogSinkAuth{Type: v1.LogSinkAuthTypeBearer, Token: token}
		})})
		require.NoError(t, err)

		return r
	}

	one, two := mk("one"), mk("two")
	assert.NotEqual(t, string(one.Content), string(two.Content))
	assert.NotContains(t, string(two.Content), "two", "secret values never appear in the config")
}

func TestRender_Empty(t *testing.T) {
	r, err := Render(nil)
	require.NoError(t, err)
	assert.Empty(t, r.Secrets)
	assert.NotContains(t, string(r.Content), "sinks")
	assert.NotContains(t, string(r.Content), "secret:")
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		sink    *v1.LogSink
		wantErr string
	}{
		{name: "valid", sink: testSink("ok", nil)},
		{name: "bad scheme", sink: testSink("x", func(vl *v1.VictoriaLogsSinkSpec) { vl.URL = "ftp://h" }), wantErr: "http(s)"},
		{name: "no host", sink: testSink("x", func(vl *v1.VictoriaLogsSinkSpec) { vl.URL = "http://" }), wantErr: "http(s)"},
		{name: "query", sink: testSink("x", func(vl *v1.VictoriaLogsSinkSpec) { vl.URL = "http://h?a=b" }), wantErr: "query"},
		{name: "basic without user", sink: testSink("x", func(vl *v1.VictoriaLogsSinkSpec) {
			vl.Auth = &v1.LogSinkAuth{Type: v1.LogSinkAuthTypeBasic}
		}), wantErr: "username"},
		{name: "bearer without token", sink: testSink("x", func(vl *v1.VictoriaLogsSinkSpec) {
			vl.Auth = &v1.LogSinkAuth{Type: v1.LogSinkAuthTypeBearer}
		}), wantErr: "token"},
		{name: "unknown auth", sink: testSink("x", func(vl *v1.VictoriaLogsSinkSpec) {
			vl.Auth = &v1.LogSinkAuth{Type: "digest"}
		}), wantErr: "auth.type"},
		{name: "extra field key with =", sink: testSink("x", func(vl *v1.VictoriaLogsSinkSpec) {
			vl.ExtraFields = map[string]string{"a=b": "c"}
		}), wantErr: "extra_fields key"},
		{name: "extra field value with comma", sink: testSink("x", func(vl *v1.VictoriaLogsSinkSpec) {
			vl.ExtraFields = map[string]string{"a": "b,c"}
		}), wantErr: "extra_fields value"},
		{name: "ca cert not PEM", sink: testSink("x", func(vl *v1.VictoriaLogsSinkSpec) {
			vl.CACert = "not a certificate"
		}), wantErr: "ca_cert"},
		{name: "bad name", sink: testSink("Bad_Name", nil), wantErr: "metadata.name"},
		{name: "wrong type", sink: func() *v1.LogSink { s := testSink("x", nil); s.Spec.Type = "loki"; return s }(), wantErr: "spec.type"},
		{name: "small buffer", sink: func() *v1.LogSink {
			s := testSink("x", nil)
			s.Spec.Buffer = &v1.LogSinkBuffer{MaxSize: 1024}
			return s
		}(), wantErr: "max_size"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.sink)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestSpecHash_CoversSecrets(t *testing.T) {
	a := testSink("a", func(vl *v1.VictoriaLogsSinkSpec) {
		vl.Auth = &v1.LogSinkAuth{Type: v1.LogSinkAuthTypeBearer, Token: "one"}
	})
	b := testSink("a", func(vl *v1.VictoriaLogsSinkSpec) {
		vl.Auth = &v1.LogSinkAuth{Type: v1.LogSinkAuthTypeBearer, Token: "two"}
	})
	assert.NotEqual(t, SpecHash(a), SpecHash(b))
	assert.Equal(t, SpecHash(a), SpecHash(a))
}

func testS3Sink(name string, mutate func(*v1.S3SinkSpec)) *v1.LogSink {
	s := &v1.LogSink{
		Metadata: &v1.Metadata{Name: name},
		Spec: &v1.LogSinkSpec{
			Type: v1.LogSinkTypeS3,
			S3: &v1.S3SinkSpec{
				Endpoint: "http://demo-s3:9000/",
				Region:   "us-east-1",
				Bucket:   "neutree-traces",
				Auth:     &v1.S3SinkAuth{AccessKeyID: "AKID", SecretAccessKey: `s3cr$t"`},
			},
		},
		Status: &v1.LogSinkStatus{ComponentID: "logsink_" + strings.ReplaceAll(name, ".", "_")},
	}
	if mutate != nil {
		mutate(s.Spec.S3)
	}

	return s
}

// The defaults render to the config verified against an S3-compatible store.
func TestRender_S3Golden(t *testing.T) {
	r, err := Render([]*v1.LogSink{testS3Sink("archive", nil)})
	require.NoError(t, err)

	_, body := splitHeader(r.Content)
	assert.Equal(t, `secret:
  logsink:
    path: /etc/vector/logsink-secrets
    type: directory
sinks:
  logsink_archive:
    auth:
      access_key_id: SECRET[logsink.access_key_id.archive]
      secret_access_key: SECRET[logsink.secret_access_key.archive]
    batch:
      timeout_secs: 300
    bucket: neutree-traces
    buffer:
      max_size: 1207959552
      type: disk
      when_full: drop_newest
    compression: gzip
    encoding:
      codec: json
    endpoint: http://demo-s3:9000
    force_path_style: true
    framing:
      method: newline_delimited
    inputs:
      - prepare_trace_payload
    key_prefix: neutree/%Y/%m/%d/
    region: us-east-1
    type: aws_s3
`, string(body))

	assert.Equal(t, map[string][]byte{
		"access_key_id.archive":     []byte(`"AKID"`),
		"secret_access_key.archive": []byte(`"s3cr$t\""`),
	}, r.Secrets)
}

func TestRender_S3Options(t *testing.T) {
	f := false
	s := testS3Sink("archive", func(s3 *v1.S3SinkSpec) {
		s3.Endpoint = ""
		s3.KeyPrefix = "traces/$HOME/%F/"
		s3.ForcePathStyle = &f
		s3.Compression = v1.LogSinkS3CompressionZstd
		s3.FlushIntervalSeconds = 10
	})
	s.Spec.Buffer = &v1.LogSinkBuffer{MaxSize: v1.LogSinkBufferMinMaxSize}

	r, err := Render([]*v1.LogSink{s})
	require.NoError(t, err)

	var generic map[string]map[string]map[string]interface{}
	require.NoError(t, yaml.Unmarshal(r.Content, &generic))

	got := generic["sinks"]["logsink_archive"]
	assert.NotContains(t, got, "endpoint", "no endpoint means the AWS endpoint of the region")
	assert.Equal(t, "traces/$$HOME/%F/", got["key_prefix"])
	assert.Equal(t, false, got["force_path_style"])
	assert.Equal(t, "zstd", got["compression"])
	assert.Equal(t, map[string]interface{}{"timeout_secs": 10}, got["batch"])
	assert.Equal(t, int(v1.LogSinkBufferMinMaxSize)+134217728, got["buffer"].(map[string]interface{})["max_size"])
}

func TestValidate_S3(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*v1.S3SinkSpec)
		wantErr string
	}{
		{name: "valid"},
		{name: "aws endpoint", mutate: func(s *v1.S3SinkSpec) { s.Endpoint = "" }},
		{name: "endpoint scheme", mutate: func(s *v1.S3SinkSpec) { s.Endpoint = "ftp://h" }, wantErr: "http(s)"},
		{name: "endpoint without host", mutate: func(s *v1.S3SinkSpec) { s.Endpoint = "http://" }, wantErr: "http(s)"},
		{name: "endpoint with credentials", mutate: func(s *v1.S3SinkSpec) { s.Endpoint = "http://a:b@h" }, wantErr: "credentials"},
		{name: "no region", mutate: func(s *v1.S3SinkSpec) { s.Region = " " }, wantErr: "region"},
		{name: "no bucket", mutate: func(s *v1.S3SinkSpec) { s.Bucket = "" }, wantErr: "bucket"},
		{name: "prefix with leading slash", mutate: func(s *v1.S3SinkSpec) { s.KeyPrefix = "/a/" }, wantErr: "key_prefix"},
		{name: "prefix with template", mutate: func(s *v1.S3SinkSpec) { s.KeyPrefix = "{{ workspace }}/" }, wantErr: "key_prefix"},
		{name: "prefix with newline", mutate: func(s *v1.S3SinkSpec) { s.KeyPrefix = "a\nb/" }, wantErr: "key_prefix"},
		{name: "prefix too long", mutate: func(s *v1.S3SinkSpec) { s.KeyPrefix = strings.Repeat("a", 513) }, wantErr: "key_prefix"},
		{name: "compression", mutate: func(s *v1.S3SinkSpec) { s.Compression = "lz4" }, wantErr: "compression"},
		{name: "negative flush", mutate: func(s *v1.S3SinkSpec) { s.FlushIntervalSeconds = -1 }, wantErr: "flush_interval_seconds"},
		{name: "no auth", mutate: func(s *v1.S3SinkSpec) { s.Auth = nil }, wantErr: "auth"},
		{name: "no secret key", mutate: func(s *v1.S3SinkSpec) { s.Auth.SecretAccessKey = "" }, wantErr: "auth"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(testS3Sink("x", tt.mutate))
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}

	missing := testS3Sink("x", nil)
	missing.Spec.S3 = nil
	assert.ErrorContains(t, Validate(missing), "spec.s3 is required")
}

func TestKeyPrefixStatic(t *testing.T) {
	assert.Equal(t, "neutree/", KeyPrefixStatic("neutree/%Y/%m/%d/"))
	assert.Equal(t, "", KeyPrefixStatic("%Y/"))
	assert.Equal(t, "a/b/", KeyPrefixStatic("a/b/"))
}
