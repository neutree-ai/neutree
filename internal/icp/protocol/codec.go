package protocol

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/neutree-ai/neutree/internal/icp/quota"
)

type StreamEncoder interface {
	WriteEvent(ev StreamEvent) error
	WriteError(err error) error
	Close() error
}

type Codec interface {
	Name() string
	EndpointURL(baseURL string) string
	DecodeRequest(r io.Reader) (CanonicalRequest, error)
	EncodeRequest(req CanonicalRequest, targetModel string) ([]byte, error)
	DecodeResponse(r io.Reader) (CanonicalResponse, error)
	EncodeResponse(resp CanonicalResponse, req CanonicalRequest) ([]byte, error)
	DecodeStream(r io.Reader, emit func(StreamEvent) error) error
	NewStreamEncoder(w io.Writer, req CanonicalRequest) StreamEncoder
}

func CodecFor(name string) (Codec, error) {
	switch name {
	case ProtocolOpenAIChat:
		return ChatCodec{}, nil
	case ProtocolAnthropicMessages:
		return AnthropicCodec{}, nil
	case ProtocolOpenAIResponses:
		return ResponsesCodec{}, nil
	default:
		return nil, fmt.Errorf("unknown protocol %q", name)
	}
}

// DetectProtocol inspects the endpoint-relative subpath to classify text-inference protocols.
// Non-matching subpaths (e.g. /v1/audio/transcriptions, /v1/embeddings, /ocr) return ("", false)
// so the proxy routes them to the zero-buffer passthrough track.
func DetectProtocol(subPath string) (string, bool) {
	clean := strings.TrimRight(subPath, "/")
	switch clean {
	case "/v1/chat/completions", "/chat/completions":
		return ProtocolOpenAIChat, true
	case "/v1/messages", "/messages", "/anthropic/v1/messages":
		return ProtocolAnthropicMessages, true
	case "/v1/responses", "/responses":
		return ProtocolOpenAIResponses, true
	default:
		return "", false
	}
}

func joinProtocolPath(baseURL, v1Suffix, bareSuffix string) string {
	trimmed := strings.TrimRight(baseURL, "/")
	for _, full := range []string{"/v1/chat/completions", "/v1/messages", "/anthropic/v1/messages", "/v1/responses"} {
		if strings.HasSuffix(trimmed, full) {
			trimmed = strings.TrimSuffix(trimmed, full)
			break
		}
	}

	if strings.HasSuffix(trimmed, "/v1") {
		return trimmed + bareSuffix
	}

	return trimmed + v1Suffix
}

type sseFrame struct {
	Event string
	Data  string
}

// readSSEFrames streams SSE frames across arbitrary Read chunk splits (\n\n and \r\n\r\n).
func readSSEFrames(r io.Reader, onFrame func(sseFrame) error) error {
	var buf [4096]byte
	var pending []byte
	scanOffset := 0

	dispatch := func(raw []byte) error {
		norm := strings.ReplaceAll(string(raw), "\r\n", "\n")
		var evName string
		var dataLines []string

		for _, line := range strings.Split(norm, "\n") {
			if line == "" || strings.HasPrefix(line, ":") {
				continue
			}

			if strings.HasPrefix(line, "event:") {
				evName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
				continue
			}

			if strings.HasPrefix(line, "data:") {
				val := strings.TrimPrefix(line, "data:")
				if strings.HasPrefix(val, " ") {
					val = val[1:]
				}

				dataLines = append(dataLines, val)
			}
		}

		if evName == "" && len(dataLines) == 0 {
			return nil
		}

		if evName == "neutree.usage" {
			return nil
		}

		return onFrame(sseFrame{
			Event: evName,
			Data:  strings.Join(dataLines, "\n"),
		})
	}

	for {
		n, err := r.Read(buf[:])
		if n > 0 {
			pending = append(pending, buf[:n]...)
		}

		for {
			start := scanOffset - 3
			if start < 0 {
				start = 0
			}

			cut := findBlankLine(pending[start:])
			if cut < 0 {
				scanOffset = len(pending)
				break
			}

			end := start + cut
			frame := pending[:end]
			pending = pending[end:]
			scanOffset = 0

			if fErr := dispatch(frame); fErr != nil {
				return fErr
			}
		}

		if err == io.EOF {
			if len(bytes.TrimSpace(pending)) > 0 {
				if fErr := dispatch(pending); fErr != nil {
					return fErr
				}
			}

			return nil
		}

		if err != nil {
			return err
		}
	}
}

func findBlankLine(b []byte) int {
	lf := bytes.Index(b, []byte("\n\n"))
	crlf := bytes.Index(b, []byte("\r\n\r\n"))

	switch {
	case lf >= 0 && (crlf < 0 || lf+2 <= crlf+4):
		return lf + 2
	case crlf >= 0:
		return crlf + 4
	default:
		return -1
	}
}

func usageMeasures(inputTokens, outputTokens, cacheCreate, cacheRead, reasoning int64) []quota.Measure {
	var m []quota.Measure
	if inputTokens > 0 {
		m = append(m, quota.Measure{Metric: "input_tokens", Unit: "tokens", Quantity: inputTokens})
	}

	if outputTokens > 0 {
		m = append(m, quota.Measure{Metric: "output_tokens", Unit: "tokens", Quantity: outputTokens})
	}

	if cacheCreate > 0 {
		m = append(m, quota.Measure{Metric: "cache_creation_input_tokens", Unit: "tokens", Quantity: cacheCreate})
	}

	if cacheRead > 0 {
		m = append(m, quota.Measure{Metric: "cache_read_input_tokens", Unit: "tokens", Quantity: cacheRead})
	}

	if reasoning > 0 {
		m = append(m, quota.Measure{Metric: "reasoning_tokens", Unit: "tokens", Quantity: reasoning})
	}

	if total := inputTokens + outputTokens; total > 0 {
		m = append(m, quota.Measure{Metric: "total_tokens", Unit: "tokens", Quantity: total})
	}

	return m
}

func measureVal(measures []quota.Measure, metric string) int64 {
	for _, m := range measures {
		if m.Metric == metric {
			return m.Quantity
		}
	}

	return 0
}
