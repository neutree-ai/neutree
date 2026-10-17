// Package logsink turns LogSink resources into a Vector config fragment, delivers
// it next to the built-in Vector config, and reads back how each sink is doing.
package logsink

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/pkg/errors"
	"gopkg.in/yaml.v3"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

const (
	// ConfigFileName must sit at the root of Vector's config dir: files in
	// subdirectories that are not named after a component kind are ignored.
	ConfigFileName = "logsinks.yaml"
	// SecretsDirName holds one extension-less file per secret, so Vector never
	// mistakes it for config.
	SecretsDirName = "logsink-secrets"
	// VectorSecretsPath is where Vector sees SecretsDirName.
	VectorSecretsPath = "/etc/vector/" + SecretsDirName

	// TraceInput is the built-in transform every external sink reads from.
	TraceInput = "prepare_trace_payload"

	componentPrefix = "logsink_"
	secretBackend   = "logsink"

	// Same as the built-in ai_trace_victorialogs sink, so records are identical.
	victoriaLogsInsertQuery = "_stream_fields=workspace,endpoint_type,endpoint_name&_time_field=timestamp"
)

var nameRegexp = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)

// ComponentID is the Vector sink ID for a LogSink. Names allow '.', which
// Vector rejects in component IDs; '_' is not allowed in names, so the mapping
// stays one-to-one.
func ComponentID(name string) string {
	return componentPrefix + strings.ReplaceAll(name, ".", "_")
}

// secretKey names a secret file. SECRET[backend.key] only matches word
// characters and '.', so '-' (never '_' in a name) is mapped to '_'.
func secretKey(kind, name string) string {
	return kind + "." + strings.ReplaceAll(name, "-", "_")
}

// Validate rejects a spec that would make Vector refuse the whole config. One
// invalid sink must not block the others, so it is excluded from rendering.
func Validate(sink *v1.LogSink) error {
	if sink.Metadata == nil || !nameRegexp.MatchString(sink.Metadata.Name) {
		return errors.New("metadata.name is invalid")
	}

	if sink.Spec == nil {
		return errors.New("spec is required")
	}

	if sink.Spec.Type != v1.LogSinkTypeVictoriaLogs {
		return errors.Errorf("unsupported spec.type %q", sink.Spec.Type)
	}

	vl := sink.Spec.VictoriaLogs
	if vl == nil {
		return errors.New("spec.victorialogs is required")
	}

	u, err := url.Parse(vl.URL)
	if err != nil {
		return errors.Wrap(err, "spec.victorialogs.url is invalid")
	}

	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("spec.victorialogs.url must be an http(s) URL with a host")
	}

	if u.RawQuery != "" || u.Fragment != "" {
		return errors.New("spec.victorialogs.url must not contain a query or fragment")
	}

	if vl.TimeoutSeconds < 0 {
		return errors.New("spec.victorialogs.timeout_seconds must not be negative")
	}

	if vl.Auth != nil {
		switch vl.Auth.Type {
		case "", v1.LogSinkAuthTypeNone:
		case v1.LogSinkAuthTypeBasic:
			if vl.Auth.Username == "" {
				return errors.New("spec.victorialogs.auth.username is required for basic auth")
			}
		case v1.LogSinkAuthTypeBearer:
			if vl.Auth.Token == "" {
				return errors.New("spec.victorialogs.auth.token is required for bearer auth")
			}
		default:
			return errors.Errorf("unsupported spec.victorialogs.auth.type %q", vl.Auth.Type)
		}
	}

	// VictoriaLogs splits extra_fields on ',' and the first '='.
	for k, v := range vl.ExtraFields {
		if k == "" || strings.ContainsAny(k, ",=") {
			return errors.Errorf("spec.victorialogs.extra_fields key %q is invalid", k)
		}

		if strings.Contains(v, ",") {
			return errors.Errorf("spec.victorialogs.extra_fields value of %q must not contain ','", k)
		}
	}

	if sink.Spec.Buffer.GetMaxSize() < v1.LogSinkBufferMinMaxSize {
		return errors.Errorf("spec.buffer.max_size must be at least %d", v1.LogSinkBufferMinMaxSize)
	}

	return nil
}

// SpecHash fingerprints everything a connection test depends on, secrets included.
func SpecHash(sink *v1.LogSink) string {
	b, _ := json.Marshal(sink.Spec) //nolint:errcheck

	sum := sha256.Sum256(b)

	return hex.EncodeToString(sum[:])
}

// Rendered is what gets delivered to Vector.
type Rendered struct {
	// Content is logsinks.yaml.
	Content []byte
	// Secrets maps a file name in SecretsDirName to its content.
	Secrets map[string][]byte
}

// Render builds logsinks.yaml for sinks that already passed Validate.
func Render(sinks []*v1.LogSink) (*Rendered, error) {
	sorted := append([]*v1.LogSink(nil), sinks...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Metadata.Name < sorted[j].Metadata.Name })

	secrets := map[string][]byte{}
	vectorSinks := map[string]interface{}{}

	for _, s := range sorted {
		vectorSinks[ComponentID(s.Metadata.Name)] = renderSink(s, secrets)
	}

	doc := map[string]interface{}{}
	if len(vectorSinks) > 0 {
		doc["sinks"] = vectorSinks
	}

	if len(secrets) > 0 {
		doc["secret"] = map[string]interface{}{
			secretBackend: map[string]interface{}{
				"type": "directory",
				"path": VectorSecretsPath,
			},
		}
	}

	var body bytes.Buffer

	if len(doc) > 0 {
		enc := yaml.NewEncoder(&body)
		enc.SetIndent(2)

		if err := enc.Encode(doc); err != nil {
			return nil, errors.Wrap(err, "failed to marshal log sink config")
		}

		if err := enc.Close(); err != nil {
			return nil, errors.Wrap(err, "failed to marshal log sink config")
		}
	}

	// Secrets live outside this file; hashing them in makes every secret change
	// visible as a content change, which is what syncers compare.
	h := sha256.New()
	h.Write(body.Bytes())

	keys := make([]string, 0, len(secrets))
	for k := range secrets {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	for _, k := range keys {
		fmt.Fprintf(h, "\x00%s\x00%s", k, secrets[k])
	}

	content := fmt.Sprintf("# Managed by neutree-core from LogSink resources. Do not edit.\n# content-hash: %s\n%s",
		hex.EncodeToString(h.Sum(nil)), body.String())

	return &Rendered{Content: []byte(content), Secrets: secrets}, nil
}

func renderSink(s *v1.LogSink, secrets map[string][]byte) map[string]interface{} {
	name := s.Metadata.Name
	vl := s.Spec.VictoriaLogs

	uri := strings.TrimSuffix(vl.URL, "/") + "/insert/jsonline?" + victoriaLogsInsertQuery

	if len(vl.ExtraFields) > 0 {
		keys := make([]string, 0, len(vl.ExtraFields))
		for k := range vl.ExtraFields {
			keys = append(keys, k)
		}

		sort.Strings(keys)

		pairs := make([]string, 0, len(keys))
		for _, k := range keys {
			pairs = append(pairs, k+"="+vl.ExtraFields[k])
		}

		uri += "&extra_fields=" + url.QueryEscape(strings.Join(pairs, ","))
	}

	request := map[string]interface{}{
		"headers": map[string]interface{}{"Content-Type": "application/stream+json"},
	}
	if vl.TimeoutSeconds > 0 {
		request["timeout_secs"] = vl.TimeoutSeconds
	}

	sink := map[string]interface{}{
		"type":        "http",
		"inputs":      []string{TraceInput},
		"uri":         escapeVars(uri),
		"method":      "post",
		"encoding":    map[string]interface{}{"codec": "json"},
		"framing":     map[string]interface{}{"method": "newline_delimited"},
		"compression": "none",
		"request":     request,
		"batch":       map[string]interface{}{"max_events": 100, "timeout_secs": 1},
		"buffer": map[string]interface{}{
			"type":      "disk",
			"max_size":  s.Spec.Buffer.GetMaxSize(),
			"when_full": "drop_newest",
		},
	}

	if vl.Auth != nil {
		switch vl.Auth.Type {
		case v1.LogSinkAuthTypeBasic:
			key := secretKey("password", name)
			secrets[key] = secretValue(vl.Auth.Password)
			sink["auth"] = map[string]interface{}{
				"strategy": "basic",
				"user":     escapeVars(vl.Auth.Username),
				"password": secretRef(key),
			}
		case v1.LogSinkAuthTypeBearer:
			key := secretKey("token", name)
			secrets[key] = secretValue(vl.Auth.Token)
			sink["auth"] = map[string]interface{}{
				"strategy": "bearer",
				"token":    secretRef(key),
			}
		}
	}

	tls := map[string]interface{}{}

	if vl.CACert != "" {
		key := secretKey("ca", name)
		secrets[key] = []byte(vl.CACert)
		tls["ca_file"] = VectorSecretsPath + "/" + key
	}

	if vl.InsecureSkipVerify {
		tls["verify_certificate"] = false
		tls["verify_hostname"] = false
	}

	if len(tls) > 0 {
		sink["tls"] = tls
	}

	return sink
}

func secretRef(key string) string {
	return "SECRET[" + secretBackend + "." + key + "]"
}

// secretValue stores a secret as a YAML double-quoted scalar: Vector pastes it
// into the config text before parsing, so a raw value could break the YAML.
func secretValue(v string) []byte {
	b, _ := json.Marshal(v) //nolint:errcheck

	return b
}

// escapeVars stops Vector from expanding $VAR in user-provided strings.
func escapeVars(s string) string {
	return strings.ReplaceAll(s, "$", "$$")
}
