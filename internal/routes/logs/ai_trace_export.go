package logs

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"k8s.io/klog/v2"

	"github.com/neutree-ai/neutree/pkg/client"
	"github.com/neutree-ai/neutree/pkg/tracecsv"
)

// maxTraceExport is the default and the maximum number of records one export
// returns.
const maxTraceExport = 100000

const (
	traceExportCSV   = "csv"
	traceExportJSONL = "jsonl"
)

// utf8BOM lets spreadsheet apps (Excel) detect UTF-8 and show CJK text right.
const utf8BOM = "\xEF\xBB\xBF"

// handleExportAITraces streams the traces matching the list route's filters,
// newest first, as a CSV or JSONL download. Records carry summary fields only,
// never request/response bodies.
func handleExportAITraces(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		store := requireTraceStore(c, deps)
		if store == nil {
			return
		}

		format := c.DefaultQuery("format", traceExportCSV)
		if format != traceExportCSV && format != traceExportJSONL {
			c.JSON(http.StatusBadRequest, gin.H{"error": "format must be csv or jsonl"})
			return
		}

		limit, ok := traceExportLimit(c.Query("limit"))
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "limit must be a positive integer"})
			return
		}

		filters, window := traceListQuery(c)
		pager := &tracePager{
			store:     store,
			scope:     traceScopeClause(c),
			filters:   filters,
			window:    window,
			remaining: limit,
			seen:      make(map[string]struct{}),
		}

		// Fetch the first page before any header goes out, so a store failure
		// still gets a normal JSON error.
		page, err := pager.next()
		if err != nil {
			klog.Errorf("ai-trace: export: %v", err)
			c.JSON(http.StatusBadGateway, gin.H{"error": "failed to query trace store"})

			return
		}

		filename := fmt.Sprintf("access-log-%s-%s.%s",
			safeFilenamePart(c.Param("workspace")), time.Now().UTC().Format("20060102T150405"), format)

		contentType := "text/csv; charset=utf-8"
		if format == traceExportJSONL {
			contentType = "application/x-ndjson"
		}

		c.Header("Content-Type", contentType)
		c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
		c.Header("X-Content-Type-Options", "nosniff")
		c.Status(http.StatusOK)

		enc, err := newTraceExportEncoder(format, c.Writer)
		if err != nil {
			klog.Errorf("ai-trace: export: write header: %v", err)
			return
		}

		for len(page) > 0 {
			for i := range page {
				if err := enc.write(page[i]); err != nil {
					klog.Errorf("ai-trace: export: write: %v", err)
					return
				}
			}

			if err := enc.flush(); err != nil {
				klog.Errorf("ai-trace: export: flush: %v", err)
				return
			}

			c.Writer.Flush()

			if c.Request.Context().Err() != nil {
				return
			}

			if page, err = pager.next(); err != nil {
				klog.Errorf("ai-trace: export: %v", err)
				return
			}
		}

		if err := enc.flush(); err != nil {
			klog.Errorf("ai-trace: export: flush: %v", err)
		}
	}
}

// traceExportLimit parses the optional limit param. Values above
// maxTraceExport are clamped; ok is false for a malformed or non-positive value.
func traceExportLimit(v string) (int, bool) {
	if v == "" {
		return maxTraceExport, true
	}

	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, false
	}

	return min(n, maxTraceExport), true
}

// safeFilenamePart keeps a path param from breaking out of the quoted
// Content-Disposition filename.
func safeFilenamePart(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		default:
			return '_'
		}
	}, s)
}

// tracePager walks the list query page by page with the inclusive time cursor.
type tracePager struct {
	store     *traceStore
	scope     string
	filters   traceFilters
	window    timeWindow
	remaining int
	seen      map[string]struct{}
	before    string
	done      bool
}

// next returns the next batch of not-yet-returned traces, or nil when the
// result set, the limit or the cursor is exhausted.
func (p *tracePager) next() ([]AITrace, error) {
	if p.done || p.remaining <= 0 {
		return nil, nil
	}

	// Always a full page: sizing it to the remaining count would let the
	// repeated boundary row crowd out the last record.
	size := maxTracePageSize

	w := p.window
	if p.before != "" {
		w.End = p.before
	}

	items, err := p.store.List(p.scope, p.filters, size, false, w)
	if err != nil {
		return nil, err
	}

	if len(items) < size {
		p.done = true
	} else {
		cursor := items[len(items)-1].Time
		// The cursor is inclusive, so a stuck cursor would return this page forever.
		if cursor == p.before {
			p.done = true
		}

		p.before = cursor
	}

	fresh := items[:0]

	for i := range items {
		if _, dup := p.seen[items[i].RequestID]; dup {
			continue
		}

		fresh = append(fresh, items[i])
		p.seen[items[i].RequestID] = struct{}{}

		if len(fresh) == p.remaining {
			break
		}
	}

	p.remaining -= len(fresh)

	// A full page of already-sent records means more rows share one timestamp
	// than a page holds; the cursor cannot move past them.
	if len(fresh) == 0 {
		p.done = true
		return nil, nil
	}

	return fresh, nil
}

// traceExportEncoder writes export records to the response body.
type traceExportEncoder interface {
	write(t AITrace) error
	flush() error
}

func newTraceExportEncoder(format string, w io.Writer) (traceExportEncoder, error) {
	if format == traceExportJSONL {
		bw := bufio.NewWriter(w)
		return &jsonlTraceEncoder{bw: bw, enc: json.NewEncoder(bw)}, nil
	}

	if _, err := io.WriteString(w, utf8BOM); err != nil {
		return nil, err
	}

	cw := csv.NewWriter(w)
	if err := cw.Write(tracecsv.Header); err != nil {
		return nil, err
	}

	return &csvTraceEncoder{cw: cw}, nil
}

type jsonlTraceEncoder struct {
	bw  *bufio.Writer
	enc *json.Encoder
}

func (e *jsonlTraceEncoder) write(t AITrace) error { return e.enc.Encode(t) }
func (e *jsonlTraceEncoder) flush() error          { return e.bw.Flush() }

type csvTraceEncoder struct {
	cw *csv.Writer
}

func (e *csvTraceEncoder) write(t AITrace) error { return e.cw.Write(tracecsv.Row(toClientTrace(t))) }

func (e *csvTraceEncoder) flush() error {
	e.cw.Flush()
	return e.cw.Error()
}

// toClientTrace maps a trace to the wire type the shared CSV layout renders.
func toClientTrace(t AITrace) client.AITrace {
	return client.AITrace{
		Upstream:         t.Upstream,
		UpstreamModel:    t.UpstreamModel,
		RequestID:        t.RequestID,
		Time:             t.Time,
		Workspace:        t.Workspace,
		EndpointType:     t.EndpointType,
		EndpointName:     t.EndpointName,
		APIKeyID:         t.APIKeyID,
		RequestURI:       t.RequestURI,
		RequestModel:     t.RequestModel,
		ResponseModel:    t.ResponseModel,
		ResponseStatus:   t.ResponseStatus,
		PromptTokens:     t.PromptTokens,
		CompletionTokens: t.CompletionTokens,
		TotalTokens:      t.TotalTokens,
		FinishReason:     t.FinishReason,
		Stream:           t.Stream,
		UserAgent:        t.UserAgent,
		DurationMs:       t.DurationMs,
		RequestBody:      t.RequestBody,
		ResponseBody:     t.ResponseBody,
		BodyTruncated:    t.BodyTruncated,
		BodyIncomplete:   t.BodyIncomplete,
	}
}
