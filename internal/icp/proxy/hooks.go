package proxy

import (
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/neutree-ai/neutree/internal/icp/protocol"
	"github.com/neutree-ai/neutree/internal/icp/router"
	"github.com/neutree-ai/neutree/internal/icp/state"
)

type RequestContext struct {
	Request  *http.Request
	Config   *state.ConfigSnapshot
	Signals  router.RouteSignals
	Wire     protocol.ID
	Envelope protocol.Envelope
	ID       string
	Status   int
	Err      error
}

type Attempt struct {
	Request *RequestContext
	Target  state.PodTarget
	Number  int
	Err     error
}

type Plugin func(*RequestContext) RequestHook
type RequestHook interface {
	OnIngress(*RequestContext) error
	OnRequest(*RequestContext) error
	OnComplete(*RequestContext)
}

type AttemptHook interface {
	OnAttemptStart(*Attempt) error
	OnAttemptResponse(*Attempt, *http.Response) error
	OnAttemptDone(*Attempt)
}

type SemanticHook interface {
	OnCanonicalResponse(*protocol.Response) error
	OnStreamEvent(*protocol.Event) error
}

type exchange struct {
	RequestContext
	clientCodec, upstreamCodec                                                 protocol.Codec
	hooks                                                                      []RequestHook
	managed, committed, terminal                                               bool
	model, consumer, upstream, upstreamModel, responseModel, messageID, finish string
	usage                                                                      *protocol.Usage
	requestBody, responseBody                                                  []byte
	started                                                                    time.Time
	replay                                                                     []byte
}

const traceLimit = 2 << 20

// Trim only once, after the last chunk: a rune may span two upstream reads.
func traceText(b []byte) string {
	if len(b) == traceLimit {
		i := len(b) - 1
		for i > len(b)-utf8.UTFMax && !utf8.RuneStart(b[i]) {
			i--
		}

		if !utf8.FullRune(b[i:]) {
			b = b[:i]
		}
	}

	return string(b)
}

func (x *exchange) captureResponse(b []byte) {
	x.responseBody = append(x.responseBody, b[:min(len(b), traceLimit-len(x.responseBody))]...)
}

func (x *exchange) record() Record {
	trace := Record{
		"request_model": x.model, "response_model": x.responseModel, "stream": x.Envelope.Stream,
		"finish_reason": x.finish, "request_body": traceText(x.requestBody), "response_body": traceText(x.responseBody),
		"upstream": x.upstream, "upstream_model": x.upstreamModel,
	}

	ai := Record{"trace": trace}
	if x.Status == 200 && x.consumer != "" && x.usage != nil && x.usage.Total > 0 {
		ai["statistics"] = Record{
			"meta":  Record{"request_model": x.model, "response_model": x.responseModel, "message_id": x.messageID},
			"usage": x.usage,
		}
	}

	return Record{
		"request":    Record{"id": x.ID, "uri": traceURI(x.Request.URL), "headers": Record{"user-agent": x.Request.UserAgent()}},
		"started_at": x.started.UnixMilli(), "latencies": Record{"request": time.Since(x.started).Milliseconds()},
		"consumer": Record{"custom_id": x.consumer}, "response": Record{"status": x.Status}, "ai": ai,
	}
}
