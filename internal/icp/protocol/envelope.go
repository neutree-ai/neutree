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
		val, ok := e.Fields[key]
		if !ok {
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

	if v := e.Fields["messages"]; v != nil {
		err = e.inspectPrefix(v)
	}

	return e, err
}

func (e *Envelope) inspectPrefix(v json.RawMessage) error {
	var msgs []struct {
		Role    string
		Content json.RawMessage
	}

	if err := json.Unmarshal(v, &msgs); err != nil {
		return err
	}

	h := fnv.New64a()
	var users, used int
	// Chained 256-byte text blocks are a demo affinity signal. Engine KV sources
	// may replace them with tokenizer-native hashes in the request hook.
	for _, m := range msgs {
		if m.Role != "system" && m.Role != "user" {
			continue
		}

		if m.Role == "user" {
			users++
			if users > 2 {
				break
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

func RewriteSame(wire ID, e Envelope, defaults map[string]json.RawMessage) ([]byte, error) {
	changed := false

	for k, v := range defaults {
		if _, ok := e.Fields[k]; !ok {
			e.Fields[k] = v
			changed = true
		}
	}

	if (wire == OpenAIChat || wire == OpenAICompletions) && e.Stream && !e.ClientUsage {
		opts := map[string]json.RawMessage{}
		if v := e.Fields["stream_options"]; v != nil && !bytes.Equal(v, []byte("null")) {
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

	return json.Marshal(e.Fields)
}
