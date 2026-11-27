package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptrace"
	"strings"
	"time"
)

func (s *Server) metrics(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")

	type sample struct {
		labels string
		values [5]float64
	}
	var samples []sample

	now := time.Now()

	for _, ep := range s.Runtime.Endpoints() {
		ws, name, _ := strings.Cut(ep, "/")

		for _, p := range s.Runtime.View(ep, now).Pods {
			qps, lat := p.Load.Stats(now)

			health := 1.
			if p.Load.CoolUntil.Load() > now.UnixNano() {
				health = 0
			}

			samples = append(samples, sample{
				labels: fmt.Sprintf("workspace=%q,endpoint=%q,server=%q,replica=%q", ws, name, p.URL, p.Replica),
				values: [5]float64{health, qps, lat, float64(p.Load.Waiting.Load()), float64(p.Load.Inflight.Load())},
			})
		}
	}

	for i, name := range [...]string{"healthy_pods_total", "current_qps", "avg_latency", "connection_pool_waiting", "num_requests_running"} {
		fmt.Fprintf(w, "# TYPE router:%s gauge\n", name)

		for _, s := range samples {
			fmt.Fprintf(w, "router:%s{%s} %g\n", name, s.labels, s.values[i])
		}
	}
}

func (s *Server) models(w http.ResponseWriter, r *http.Request, ep string) {
	seen := make(map[string]bool)
	models := []json.RawMessage{}
	pods := s.Runtime.View(ep, time.Now()).Pods
	successful := 0

	for _, pod := range pods {
		if r.Context().Err() != nil {
			return
		}

		if pod.Load.CoolUntil.Load() > time.Now().UnixNano() {
			continue
		}

		ctx, cancel := context.WithCancelCause(r.Context())
		stop := deadline(s.Options.Connect, cancel)

		err := pod.Load.Acquire(ctx)
		if err == nil {
			err = func() error {
				defer pod.Load.Release()

				ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GotConn: func(httptrace.GotConnInfo) { stop() }})

				req, err := upstreamRequest(r, ctx, pod.URL, "/v1/models", nil)
				if err != nil {
					return err
				}

				resp, err := s.transport.RoundTrip(req)
				if err != nil {
					coolOnDialFailure(pod.Load, err)
					return err
				}

				defer resp.Body.Close()

				if resp.StatusCode != 200 {
					return fmt.Errorf("model list: %s", resp.Status)
				}

				b, err := readBounded(&idleReader{Reader: resp.Body, idle: s.Options.Idle, cancel: cancel}, 4<<20)
				if err != nil {
					return err
				}

				var page struct{ Data []json.RawMessage }
				if err = json.Unmarshal(b, &page); err != nil {
					return err
				}

				ids := make([]string, len(page.Data))

				for i, raw := range page.Data {
					var card struct{ ID string }
					if err = json.Unmarshal(raw, &card); err != nil {
						return err
					}

					ids[i] = card.ID
				}

				for i, raw := range page.Data {
					if !seen[ids[i]] {
						seen[ids[i]] = true

						models = append(models, raw)
					}
				}

				return nil
			}()
		}

		stop()
		cancel(nil)

		if err == nil {
			successful++
		}
	}

	if successful == 0 {
		http.Error(w, "could not retrieve backend model list", 502)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	if err := json.NewEncoder(w).Encode(Record{"object": "list", "data": models}); err != nil {
		panic(http.ErrAbortHandler)
	}
}
