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
	}
	if mutate != nil {
		mutate(s.Spec.VictoriaLogs)
	}

	return s
}

func TestComponentIDAndSecretKey(t *testing.T) {
	assert.Equal(t, "logsink_a-b_c", ComponentID("a-b.c"))
	assert.Equal(t, "token.a_b.c", secretKey("token", "a-b.c"))
	// '.' and '-' map to different characters, so distinct names stay distinct.
	assert.NotEqual(t, ComponentID("a.b"), ComponentID("a-b"))
	assert.NotEqual(t, secretKey("token", "a.b"), secretKey("token", "a-b"))
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
	assert.Equal(t, map[string]interface{}{"type": "disk", "max_size": 1073741824, "when_full": "drop_newest"}, b["buffer"])
	assert.Equal(t, map[string]interface{}{"max_events": 100, "timeout_secs": 1}, b["batch"])

	a := generic["sinks"]["logsink_audit_vl"]
	assert.Equal(t, map[string]interface{}{"strategy": "bearer", "token": "SECRET[logsink.token.audit.vl]"}, a["auth"])
	assert.Equal(t, map[string]interface{}{"verify_certificate": false, "verify_hostname": false}, a["tls"])
	assert.Equal(t, map[string]interface{}{"headers": map[string]interface{}{"Content-Type": "application/stream+json"}}, a["request"])
	assert.Equal(t, 536870912, a["buffer"].(map[string]interface{})["max_size"])

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
