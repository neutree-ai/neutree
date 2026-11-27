package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/neutree-ai/neutree/internal/icp/protocol"
)

func (x *exchange) semanticEvent(ev *protocol.Event) error {
	for _, h := range x.hooks {
		if sh, ok := h.(SemanticHook); ok {
			if err := sh.OnStreamEvent(ev); err != nil {
				return err
			}
		}
	}

	return nil
}

func (x *exchange) streamResponse(body *idleReader, deliver func([]byte, bool) error) error {
	var decoder protocol.StreamDecoder
	var encoder protocol.StreamEncoder

	if x.clientCodec != nil {
		decoder = x.upstreamCodec.NewStreamDecoder()
		encoder = x.clientCodec.NewStreamEncoder(protocol.EncodeOpts{ID: x.ID, Model: x.model, IncludeUsage: x.Envelope.ClientUsage})
	}

	sequence := 0
	var tail *time.Timer

	err := protocol.ScanSSE(body, func(f protocol.Frame) error {
		if tail != nil {
			return nil
		}

		if decoder != nil {
			return decoder.Push(f, func(ev protocol.Event) error {
				if ev.Usage != nil {
					x.usage = ev.Usage
				}

				if err := x.semanticEvent(&ev); err != nil {
					return err
				}

				return encoder.Push(ev, func(out protocol.Frame) error {
					if err := deliver(out.Raw, true); err != nil {
						return err
					}

					sequence++

					return nil
				})
			})
		}

		err := x.forwardFrame(f, deliver, &sequence)
		if err == nil && x.terminal {
			// Read the HTTP trailer to recycle the connection, even after the protocol has ended.
			tail = time.AfterFunc(100*time.Millisecond, func() { body.cancel(context.DeadlineExceeded) })
		}

		return err
	})

	if tail != nil {
		tail.Stop()
		return nil
	}

	if err == nil && decoder != nil {
		err = decoder.Finish()
		x.terminal = err == nil
	}

	if err == nil && !x.terminal {
		err = io.ErrUnexpectedEOF
	}

	if err != nil && x.committed && !x.terminal && x.Request.Context().Err() == nil {
		x.finish = errorType
		_ = deliver(protocol.ErrorFrame(x.Wire, sequence, "upstream stream interrupted"), true)
	}

	return err
}

func (x *exchange) forwardFrame(f protocol.Frame, deliver func([]byte, bool) error, sequence *int) error {
	if f.HasData {
		if f.Name == errorType || f.Name == "response.failed" {
			x.finish = errorType
			x.terminal = true
		}

		switch x.Wire {
		case protocol.OpenAIChat, protocol.OpenAICompletions, protocol.OpenAIEmbeddings, protocol.OpenAIRerank:
			if bytes.Equal(f.Data, []byte("[DONE]")) {
				x.terminal = true
			} else {
				var chunk protocol.ChatResult
				if err := json.Unmarshal(f.Data, &chunk); err != nil {
					return err
				}

				if chunk.Error != nil {
					x.terminal = true
					x.finish = errorType
				}

				x.observe(chunk)

				ev := protocol.Event{Kind: "chunk", Raw: f.Data, Usage: chunk.Usage}
				if err := x.semanticEvent(&ev); err != nil {
					return err
				}

				if (x.Wire == protocol.OpenAIChat || x.Wire == protocol.OpenAICompletions) && !x.Envelope.ClientUsage && chunk.Usage != nil && len(chunk.Choices) == 0 {
					return nil
				}
			}
		case protocol.AnthropicMessages:
			if f.Name == "message_stop" {
				x.terminal = true
			}
		case protocol.OpenAIResponses:
			var e struct {
				Sequence int `json:"sequence_number"`
			}

			if json.Unmarshal(f.Data, &e) == nil {
				*sequence = e.Sequence + 1
			}

			if f.Name == "response.completed" || f.Name == "response.incomplete" {
				x.terminal = true
			}
		}
	}

	return deliver(f.Raw, true)
}

func (x *exchange) jsonResponse(body io.Reader) ([]byte, error) {
	b, err := readBounded(body, 16<<20)
	if err != nil {
		return nil, err
	}

	if x.Wire == "" {
		// A custom JSON payload is still forwarded verbatim when it has no OpenAI usage schema.
		var meta struct {
			Model string
			Usage *protocol.Usage
		}

		if json.Unmarshal(b, &meta) == nil {
			x.responseModel = meta.Model
			x.usage = meta.Usage
		}

		return b, nil
	}

	var canonical protocol.Response
	if x.upstreamCodec != nil {
		canonical, err = x.upstreamCodec.DecodeResponse(b)
		if err != nil {
			return nil, err
		}

		x.usage = canonical.Usage
		x.messageID = canonical.ID
		x.responseModel = canonical.Model
		x.finish = canonical.FinishReason
	} else {
		var c protocol.ChatResult
		if err = json.Unmarshal(b, &c); err != nil {
			return nil, err
		}

		x.observe(c)

		canonical = protocol.Response{ID: c.ID, Model: c.Model, Usage: c.Usage, FinishReason: x.finish}
		for _, choice := range c.Choices {
			canonical.Blocks = append(canonical.Blocks, protocol.Block{Kind: "message", Data: choice.Message.Content})
		}
	}

	for _, h := range x.hooks {
		if sh, ok := h.(SemanticHook); ok {
			if err := sh.OnCanonicalResponse(&canonical); err != nil {
				return nil, err
			}
		}
	}

	if x.clientCodec != nil {
		return x.clientCodec.EncodeResponse(canonical, protocol.EncodeOpts{ID: x.ID, Model: x.model})
	}

	return b, nil
}

func (x *exchange) observe(c protocol.ChatResult) {
	if c.ID != "" {
		x.messageID = c.ID
	}

	if c.Model != "" {
		x.responseModel = c.Model
	}

	if c.Usage != nil {
		x.usage = c.Usage
	}

	if len(c.Choices) > 0 && c.Choices[0].FinishReason != "" {
		x.finish = c.Choices[0].FinishReason
	}
}
