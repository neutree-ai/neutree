package quota

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

const usageHeader = "X-Neutree-Usage"

type usageToken struct {
	RequestID  string   `json:"request_id"`
	AttemptID  string   `json:"attempt_id"`
	APIKeyID   string   `json:"api_key_id"`
	PoolIDs    []string `json:"pool_ids,omitempty"`
	RouteKey   string   `json:"route_key,omitempty"`
	UpstreamID string   `json:"upstream_id,omitempty"`
	Model      string   `json:"model,omitempty"`
	ExpiresAt  int64    `json:"expires_at"`
}

func SignUsageToken(record UsageRecord, expiresAt time.Time, secret []byte) (string, error) {
	p := usageToken{
		RequestID:  record.RequestID,
		AttemptID:  record.AttemptID,
		APIKeyID:   record.APIKeyID,
		PoolIDs:    append([]string(nil), record.PoolIDs...),
		RouteKey:   record.RouteKey,
		UpstreamID: record.UpstreamID,
		Model:      record.Model,
		ExpiresAt:  expiresAt.Unix(),
	}

	b, err := json.Marshal(p)
	if err != nil {
		return "", err
	}

	enc := base64.RawURLEncoding.EncodeToString(b)
	h := hmac.New(sha256.New, secret)
	_, _ = h.Write([]byte(enc))

	return enc + "." + base64.RawURLEncoding.EncodeToString(h.Sum(nil)), nil
}

func VerifyUsageCallback(body []byte, token string, secret []byte) (UsageRecord, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return UsageRecord{}, errors.New("invalid usage token")
	}

	h := hmac.New(sha256.New, secret)
	_, _ = h.Write([]byte(parts[0]))

	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(sig, h.Sum(nil)) {
		return UsageRecord{}, errors.New("invalid usage signature")
	}

	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return UsageRecord{}, err
	}

	var p usageToken
	if err = json.Unmarshal(raw, &p); err != nil || time.Now().Unix() >= p.ExpiresAt {
		return UsageRecord{}, errors.New("expired usage token")
	}

	var input struct {
		Measures []Measure `json:"measures"`
	}

	if err = json.Unmarshal(body, &input); err != nil {
		return UsageRecord{}, err
	}

	if BillableUnits(input.Measures) < 0 {
		return UsageRecord{}, errors.New("negative usage")
	}

	return UsageRecord{
		RequestID:  p.RequestID,
		AttemptID:  p.AttemptID,
		APIKeyID:   p.APIKeyID,
		PoolIDs:    p.PoolIDs,
		RouteKey:   p.RouteKey,
		UpstreamID: p.UpstreamID,
		Model:      p.Model,
		Measures:   input.Measures,
		Source:     "async_callback",
		CreatedAt:  time.Now().UTC(),
	}, nil
}

func ExtractHeaderUsage(h http.Header) (UsageRecord, bool, error) {
	v := h.Get(usageHeader)
	if v == "" {
		return UsageRecord{}, false, nil
	}

	h.Del(usageHeader)

	var record UsageRecord
	if err := json.Unmarshal([]byte(v), &record); err != nil {
		return UsageRecord{}, false, err
	}

	if BillableUnits(record.Measures) < 0 {
		return UsageRecord{}, false, errors.New("negative usage")
	}

	record.Source = "header"

	return record, true, nil
}

type SSEUsageFilter struct {
	source     io.Reader
	pending    []byte
	frame      []byte
	scanOffset int
	usage      UsageRecord
	hasUsage   bool
	eof        bool
}

func NewSSEUsageFilter(r io.Reader) *SSEUsageFilter {
	return &SSEUsageFilter{source: r}
}

func (f *SSEUsageFilter) Usage() (UsageRecord, bool) {
	return f.usage, f.hasUsage
}

func (f *SSEUsageFilter) Read(p []byte) (int, error) {
	for len(f.pending) == 0 && !f.eof {
		if err := f.readFrame(); err != nil && len(f.pending) == 0 {
			return 0, err
		}
	}

	if len(f.pending) == 0 {
		return 0, io.EOF
	}

	n := copy(p, f.pending)
	f.pending = f.pending[n:]

	return n, nil
}

func (f *SSEUsageFilter) readFrame() error {
	var buf [4096]byte
	n, err := f.source.Read(buf[:])

	if n > 0 {
		f.frame = append(f.frame, buf[:n]...)
	}

	for {
		start := f.scanOffset - 3
		if start < 0 {
			start = 0
		}

		rel := nextSSEBoundary(f.frame[start:])
		if rel < 0 {
			f.scanOffset = len(f.frame)
			break
		}

		cut := start + rel
		frame := append([]byte(nil), f.frame[:cut]...)
		f.frame = f.frame[cut:]
		f.scanOffset = 0
		f.consume(frame)
	}

	if err == io.EOF {
		f.eof = true
		if len(f.frame) > 0 {
			f.consume(f.frame)
			f.frame = nil
			f.scanOffset = 0
		}
	}

	return err
}

func nextSSEBoundary(b []byte) int {
	lfIdx := bytes.Index(b, []byte("\n\n"))
	crlfIdx := bytes.Index(b, []byte("\r\n\r\n"))

	switch {
	case lfIdx >= 0 && (crlfIdx < 0 || lfIdx+2 <= crlfIdx+4):
		return lfIdx + 2
	case crlfIdx >= 0:
		return crlfIdx + 4
	default:
		return -1
	}
}

func (f *SSEUsageFilter) consume(frame []byte) {
	isUsage := false
	var dataParts []string

	normalized := strings.ReplaceAll(string(frame), "\r\n", "\n")
	for _, line := range strings.Split(normalized, "\n") {
		if strings.HasPrefix(line, "event:") && strings.TrimSpace(strings.TrimPrefix(line, "event:")) == "neutree.usage" {
			isUsage = true
			continue
		}

		if strings.HasPrefix(line, "data:") {
			dataParts = append(dataParts, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}

	if !isUsage {
		f.pending = append(f.pending, frame...)
		return
	}

	rawData := strings.Join(dataParts, "\n")
	if rawData == "" {
		return
	}

	var r UsageRecord
	if err := json.Unmarshal([]byte(rawData), &r); err == nil && len(r.Measures) > 0 && BillableUnits(r.Measures) >= 0 {
		r.Source = "sse"
		f.usage = r
		f.hasUsage = true
	}
}
