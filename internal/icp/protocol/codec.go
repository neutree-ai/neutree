package protocol

import "encoding/json"

type Block struct {
	Kind, Text, ID, Name, Signature string
	Data                            json.RawMessage
	Source                          ID
	Content                         []Block // tool result content, including images
	IsError                         bool
}

type Message struct {
	Role   string
	Blocks []Block
}

type Request struct {
	Model      string
	Stream     bool
	Messages   []Message
	Tools      []Tool
	ToolChoice string
	ToolName   string
	Parallel   *bool
	Params     map[string]json.RawMessage // canonical keys: temperature, top_p, max_tokens, stop
}

type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

type Response struct {
	ID, Model, FinishReason string
	Blocks                  []Block
	Usage                   *Usage
}

type Event struct {
	ID, Model, FinishReason string
	Kind                    string
	Index                   int
	Block                   Block
	Usage                   *Usage
	Raw                     json.RawMessage
}

type EncodeOpts struct {
	Created      int64
	ID, Model    string
	IncludeUsage bool
	Defaults     map[string]json.RawMessage
}

type Codec interface {
	DecodeRequest(Envelope) (Request, error)
	EncodeRequest(Request, EncodeOpts) ([]byte, error)
	DecodeResponse([]byte) (Response, error)
	EncodeResponse(Response, EncodeOpts) ([]byte, error)
	NewStreamDecoder() StreamDecoder
	NewStreamEncoder(EncodeOpts) StreamEncoder
}

type StreamDecoder interface {
	Push(Frame, func(Event) error) error
	Finish() error
}

type StreamEncoder interface {
	Push(Event, func(Frame) error) error
}

type Usage struct {
	Prompt     int64    `json:"prompt_tokens"`
	Completion int64    `json:"completion_tokens"`
	Total      int64    `json:"total_tokens"`
	CacheRead  *int64   `json:"cache_read_tokens,omitempty"`
	CacheWrite *int64   `json:"cache_creation_tokens,omitempty"`
	Reasoning  *int64   `json:"reasoning_tokens,omitempty"`
	Cost       *float64 `json:"cost,omitempty"`
}

type ChatResult struct {
	ID, Model string
	Choices   []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct{ Content json.RawMessage }
		Delta        struct{ Content json.RawMessage }
	}
	Usage *Usage
	Error json.RawMessage
}

func ErrorFrame(id ID, sequence int, message string) []byte {
	var body any

	name := ""

	switch id {
	case AnthropicMessages:
		name = "event: error\n"
		body = map[string]any{"type": "error", "error": map[string]string{"type": "api_error", "message": message}}
	case OpenAIResponses:
		name = "event: error\n"
		body = map[string]any{"type": "error", "code": "upstream_error", "message": message, "param": nil, "sequence_number": sequence}
	default:
		body = map[string]any{"error": map[string]string{"type": "server_error", "code": "upstream_error", "message": message}}
	}

	b, _ := json.Marshal(body)

	return append(append([]byte(name+"data: "), b...), '\n', '\n')
}
