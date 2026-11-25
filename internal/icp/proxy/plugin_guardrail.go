package proxy

import (
	"errors"
	"net/http"
	"strings"

	"github.com/neutree-ai/neutree/internal/icp/protocol"
)

const (
	errTypeGuardrail = "content_policy_violation"
	errCodeGuardrail = "guardrail_blocked"
	redactedMask     = "[REDACTED]"
)

var ErrGuardrailBlocked = errors.New("output content blocked by guardrail policy")

type GuardrailPlugin struct {
	Block  []string
	Redact []string
}

func NewGuardrailPlugin(block, redact []string) *GuardrailPlugin {
	return &GuardrailPlugin{
		Block:  cleanTerms(block),
		Redact: cleanTerms(redact),
	}
}

func (p *GuardrailPlugin) Name() string {
	return "guardrail"
}

func (p *GuardrailPlugin) Begin(rc *RequestContext) RequestHook {
	if (len(p.Block) == 0 && len(p.Redact) == 0) || rc.Track != TrackTranslated || rc.IsModelList {
		return nil
	}

	return &guardrailSession{plugin: p}
}

type guardrailSession struct {
	plugin *GuardrailPlugin
}

func (s *guardrailSession) OnIngress(_ *RequestContext) *Response {
	return nil
}

func (s *guardrailSession) OnRequest(rc *RequestContext) *Response {
	if rc.CanonicalRequest == nil {
		return nil
	}

	if s.plugin.inspectBlocks(rc.CanonicalRequest.System) {
		return errorResponse(http.StatusBadRequest, errTypeGuardrail, errCodeGuardrail, "request system prompt blocked by guardrail policy")
	}

	for i := range rc.CanonicalRequest.Messages {
		if s.plugin.inspectBlocks(rc.CanonicalRequest.Messages[i].Content) {
			return errorResponse(http.StatusBadRequest, errTypeGuardrail, errCodeGuardrail, "request content blocked by guardrail policy")
		}
	}

	return nil
}

func (s *guardrailSession) OnComplete(_ *RequestContext) {}

func (s *guardrailSession) OnCanonicalResponse(_ *AttemptContext, resp *protocol.CanonicalResponse) *Response {
	if s.plugin.inspectBlocks(resp.Content) {
		return errorResponse(http.StatusBadRequest, errTypeGuardrail, errCodeGuardrail, ErrGuardrailBlocked.Error())
	}

	return nil
}

func (s *guardrailSession) OnStreamEvent(_ *AttemptContext, ev *protocol.StreamEvent) error {
	hit, rewritten := s.plugin.applyText(ev.Text)
	if hit {
		return ErrGuardrailBlocked
	}

	ev.Text = rewritten

	if ev.ToolCall != nil {
		argHit, argRewritten := s.plugin.applyText(ev.ToolCall.Arguments)
		if argHit {
			return ErrGuardrailBlocked
		}

		ev.ToolCall.Arguments = argRewritten
	}

	return nil
}

func (p *GuardrailPlugin) inspectBlocks(blocks []protocol.ContentBlock) bool {
	for i := range blocks {
		hit, rewritten := p.applyText(blocks[i].Text)
		if hit {
			return true
		}

		blocks[i].Text = rewritten

		if blocks[i].ToolArguments != "" {
			argHit, argRewritten := p.applyText(blocks[i].ToolArguments)
			if argHit {
				return true
			}

			blocks[i].ToolArguments = argRewritten
		}
	}

	return false
}

func (p *GuardrailPlugin) applyText(text string) (bool, string) {
	if text == "" {
		return false, text
	}

	lower := strings.ToLower(text)
	for _, word := range p.Block {
		if strings.Contains(lower, strings.ToLower(word)) {
			return true, text
		}
	}

	out := text
	for _, word := range p.Redact {
		out = replaceCaseInsensitive(out, word, redactedMask)
	}

	return false, out
}

func replaceCaseInsensitive(input, needle, replacement string) string {
	if needle == "" {
		return input
	}

	lowerInput := strings.ToLower(input)
	lowerNeedle := strings.ToLower(needle)

	var b strings.Builder
	pos := 0

	for {
		idx := strings.Index(lowerInput[pos:], lowerNeedle)
		if idx < 0 {
			b.WriteString(input[pos:])
			break
		}

		b.WriteString(input[pos : pos+idx])
		b.WriteString(replacement)

		pos += idx + len(needle)
	}

	return b.String()
}

func cleanTerms(in []string) []string {
	out := make([]string, 0, len(in))

	for _, v := range in {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			out = append(out, trimmed)
		}
	}

	return out
}
