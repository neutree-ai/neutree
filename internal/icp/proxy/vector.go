package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"
)

type Record map[string]any
type UsageSink interface {
	Enqueue(Record) bool
	Drain(context.Context) error
}

type VectorSink struct {
	queue           chan Record
	done            chan struct{}
	stop            context.CancelFunc
	Dropped, Failed atomic.Uint64
}

func NewVectorSink(endpoint string, capacity int) *VectorSink {
	ctx, cancel := context.WithCancel(context.Background())
	s := &VectorSink{queue: make(chan Record, capacity), done: make(chan struct{}), stop: cancel}

	go func() {
		defer close(s.done)

		client := &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

		for {
			select {
			case <-ctx.Done():
				return
			case rec, ok := <-s.queue:
				if !ok {
					return
				}

				b, err := json.Marshal(rec)
				if err == nil {
					for attempt := 0; attempt < 2; attempt++ {
						var req *http.Request

						req, err = http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(b))
						if err != nil {
							break
						}

						req.Header.Set("Content-Type", "application/json")
						var resp *http.Response

						resp, err = client.Do(req)
						if err == nil {
							// Drain for connection reuse; delivery is acknowledged by the HTTP status.
							_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
							resp.Body.Close()

							if resp.StatusCode/100 == 2 {
								break
							}

							err = fmt.Errorf("Vector returned %s", resp.Status)

							if resp.StatusCode < 500 {
								break
							}
						}
					}
				}

				if err != nil {
					s.Failed.Add(1)
					slog.Warn("Vector delivery failed", "error", err)
				}
			}
		}
	}()

	return s
}

func (s *VectorSink) Enqueue(r Record) bool {
	select {
	case s.queue <- r:
		return true
	default:
		s.Dropped.Add(1)
		return false
	}
}

// Drain is called once after HTTP handlers have finished publishing.
func (s *VectorSink) Drain(ctx context.Context) error {
	close(s.queue)
	defer s.stop()
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
