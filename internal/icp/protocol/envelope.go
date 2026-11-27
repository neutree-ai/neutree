package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"strings"
)

type ID string

const (
	OpenAICompletions ID = "openai.completions"
	OpenAIEmbeddings  ID = "openai.embeddings"
	OpenAIRerank      ID = "openai.rerank"
	OpenAIChat        ID = "openai.chat"
	AnthropicMessages ID = "anthropic.messages"
	OpenAIResponses   ID = "openai.responses"
)

func Detect(path string) ID {
	switch strings.TrimSuffix(path, "/") {
	case "/v1/completions":
		return OpenAICompletions
	case "/v1/embeddings":
		return OpenAIEmbeddings
	case "/v1/rerank":
		return OpenAIRerank
	case "/v1/chat/completions":
		return OpenAIChat
	case "/anthropic/v1/messages", "/v1/messages":
		return AnthropicMessages
	case "/v1/responses":
		return OpenAIResponses
	}

	return ""
}

type Envelope struct {
	Raw                            []byte
	Fields                         map[string]json.RawMessage
	Model, Session                 string
	Stream, ClientUsage, HasPrefix bool
	PrefixHash                     uint64
	BlockHashes                    []uint64
}

func isNull(raw json.RawMessage) bool {
	return len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func Inspect(raw []byte) (Envelope, error) {
	e := Envelope{Raw: raw, Fields: map[string]json.RawMessage{}}
	d := json.NewDecoder(bytes.NewReader(raw))

	tok, err := d.Token()
	if err != nil || tok != json.Delim('{') {
		return e, errors.New("expected JSON object")
	}

	for d.More() {
		token, err := d.Token()
		if err != nil {
			return e, err
		}

		k, ok := token.(string)
		if !ok {
			return e, errors.New("expected JSON field name")
		}

		if _, exists := e.Fields[k]; exists {
			return e, fmt.Errorf("duplicate field %q", k)
		}

		if strings.EqualFold(k, "model") && k != "model" || strings.EqualFold(k, "stream") && k != "stream" {
			return e, fmt.Errorf("invalid field spelling %q", k)
		}

		var val json.RawMessage
		if err = d.Decode(&val); err != nil {
			return e, err
		}

		e.Fields[k] = val
	}

	if _, err = d.Token(); err != nil {
		return e, err
	}

	if _, err := d.Token(); err != io.EOF {
		return e, errors.New("unexpected trailing input")
	}

	for _, key := range []string{"model", "stream", "stream_options", "session_id", "prompt_cache_key"} {
		val := e.Fields[key]
		if isNull(val) {
			continue
		}

		switch key {
		case "model":
			err = json.Unmarshal(val, &e.Model)
		case "stream":
			err = json.Unmarshal(val, &e.Stream)
		case "stream_options":
			var opts struct {
				Include bool `json:"include_usage"`
			}
			err = json.Unmarshal(val, &opts)
			e.ClientUsage = opts.Include
		default:
			if e.Session == "" {
				err = json.Unmarshal(val, &e.Session)
			}
		}

		if err != nil {
			return e, fmt.Errorf("%s: %w", key, err)
		}
	}

	err = e.inspectPrefix()

	return e, err
}

type prefixMessage struct {
	Role    string
	Content json.RawMessage
}

func (e *Envelope) inspectPrefix() error {
	var msgs []prefixMessage
	// Embedding inputs may be token IDs. Only Responses message items carry roles.
	if raw := e.Fields["messages"]; raw != nil {
		if err := json.Unmarshal(raw, &msgs); err != nil {
			return err
		}
	} else if raw := e.Fields["input"]; len(raw) > 0 {
		switch raw[0] {
		case '"':
			msgs = []prefixMessage{{Role: roleUser, Content: raw}}
		case '[':
			items := bytes.TrimSpace(raw[1:])
			if len(items) > 0 && items[0] == '{' {
				if err := json.Unmarshal(raw, &msgs); err != nil {
					return err
				}
			}
		}
	}

	system := e.Fields["system"]
	if isNull(system) {
		system = e.Fields["instructions"]
	}

	if !isNull(system) {
		msgs = append([]prefixMessage{{roleSystem, system}}, msgs...)
	}

	if len(msgs) == 0 {
		return nil
	}

	h := fnv.New64a()
	var users, used int
	// Chained 256-byte text blocks are a demo affinity signal. Engine KV sources
	// may replace them with tokenizer-native hashes in the request hook.
	for _, m := range msgs {
		if m.Role == "developer" {
			m.Role = roleSystem
		}

		if m.Role != roleSystem && m.Role != roleUser {
			continue
		}

		if m.Role == roleUser {
			users++
			if users > 2 {
				continue
			}
		}
		var compact bytes.Buffer

		if len(m.Content) == 0 {
			continue
		}

		if err := json.Compact(&compact, m.Content); err != nil {
			return err
		}

		for _, part := range [][]byte{[]byte(m.Role), {0}, compact.Bytes(), {0}} {
			for len(part) > 0 {
				n := min(256-used, len(part))
				h.Write(part[:n])
				part = part[n:]

				used += n
				if used == 256 {
					if len(e.BlockHashes) < 32 {
						e.BlockHashes = append(e.BlockHashes, h.Sum64())
					}

					used = 0
				}
			}
		}

		e.HasPrefix = true
	}

	if e.HasPrefix {
		e.PrefixHash = h.Sum64()
		if used > 0 && len(e.BlockHashes) < 32 {
			e.BlockHashes = append(e.BlockHashes, e.PrefixHash)
		}
	}

	return nil
}

func applyModelDefaults(wire ID, fields, defaults map[string]json.RawMessage) (bool, error) {
	changed := false

	for k, v := range defaults {
		if isNull(v) {
			continue
		}

		if wire == OpenAIChat && (k == maxTokens || k == completionMaxTokens || k == responsesMaxTokens) {
			if !isNull(fields[maxTokens]) || !isNull(fields[completionMaxTokens]) {
				continue
			}

			if k == responsesMaxTokens {
				k = maxTokens
			}
		} else if wire.Path() != "" {
			var err error

			k, v, err = wireParam(wire, k, v)
			if err != nil {
				return false, err
			}
		}

		if isNull(fields[k]) {
			fields[k] = v
			changed = true
		}
	}

	return changed, nil
}

func RewriteSame(wire ID, e Envelope, defaults map[string]json.RawMessage) ([]byte, error) {
	changed, err := applyModelDefaults(wire, e.Fields, defaults)
	if err != nil {
		return nil, err
	}

	if wire.Path() != "" {
		if err := ValidateRequest(wire, e); err != nil {
			return nil, err
		}
		// Validation accepts references only when null. Omit those SDK placeholders on the wire.
		for _, key := range nullableReferences {
			if _, ok := e.Fields[key]; ok {
				delete(e.Fields, key)

				changed = true
			}
		}
	}

	if wire == OpenAIResponses {
		updated, err := applyResponseDefaults(e.Fields)
		if err != nil {
			return nil, err
		}

		changed = changed || updated
	}

	if wire == AnthropicMessages && isNull(e.Fields[maxTokens]) {
		e.Fields["max_tokens"] = json.RawMessage("4096")
		changed = true
	}

	if (wire == OpenAIChat || wire == OpenAICompletions) && e.Stream && !e.ClientUsage {
		opts := map[string]json.RawMessage{}
		if v := e.Fields["stream_options"]; !isNull(v) {
			if err := json.Unmarshal(v, &opts); err != nil {
				return nil, err
			}
		}

		opts["include_usage"] = json.RawMessage("true")
		e.Fields["stream_options"], _ = json.Marshal(opts)
		changed = true
	}

	if !changed {
		return e.Raw, nil
	}

	if wire == OpenAIChat {
		for _, key := range [...]string{maxTokens, completionMaxTokens} {
			if isNull(e.Fields[key]) {
				delete(e.Fields, key)
			}
		}
	}

	return json.Marshal(e.Fields)
}

// Responses enables storage and strict function schemas when those flags are omitted.
func applyResponseDefaults(fields map[string]json.RawMessage) (bool, error) {
	changed := false

	if isNull(fields["store"]) {
		fields["store"] = json.RawMessage("false")
		changed = true
	}

	if raw := fields["tools"]; !isNull(raw) {
		var tools []map[string]json.RawMessage
		if err := json.Unmarshal(raw, &tools); err != nil {
			return false, err
		}

		rewrite := false

		for _, tool := range tools {
			if isNull(tool["strict"]) {
				tool["strict"] = json.RawMessage("false")
				rewrite = true
			}
		}

		if rewrite {
			fields["tools"], _ = json.Marshal(tools)
			changed = true
		}
	}

	return changed, nil
}
