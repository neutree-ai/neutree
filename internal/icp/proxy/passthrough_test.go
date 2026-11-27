package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/neutree-ai/neutree/internal/icp/event"
	"github.com/neutree-ai/neutree/internal/icp/state"
)

func TestL0AndModelListsDelegateUnmarked(t *testing.T) {
	for _, tc := range []struct{ mode, method, path string }{
		{L0, "POST", chatPath}, {Front, "GET", "/workspace/ws/external-endpoint/ep/anthropic/v1/models"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			wire := []byte{0xff, 0, 0xfe}
			kong := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.EscapedPath() != tc.path || r.Header.Get(internal+"Unpacked") != "" || r.Header.Get("kong_apikey") != "" || r.URL.RawQuery != "keep=1" {
					t.Error("invalid relay", r.URL, r.Header)
				}
				if r.Method == "POST" {
					b, _ := io.ReadAll(r.Body)
					if !bytes.Equal(b, wire) {
						t.Errorf("upstream bytes: %x", b)
					}
				}
				w.Header().Set(internal+"From-Upstream", "private")
				w.Write(wire)
			}))
			defer kong.Close()
			app := appFor(t, tc.mode, kong.URL)
			r := httptest.NewRequest(tc.method, tc.path+"?kong_apikey=key&keep=1", bytes.NewReader(wire))
			r.Header.Set(internal+"Unpacked", "1")
			r.Header.Set("kong_apikey", "key")
			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, r)
			if !bytes.Equal(rec.Body.Bytes(), wire) || rec.Header().Get(internal+"From-Upstream") != "" {
				t.Fatal(rec.Result())
			}
		})
	}
}
func TestRouterAggregatesModelsDuringRollout(t *testing.T) {
	s := appFor(t, Cluster, "")
	s.Options.Connect = 100 * time.Millisecond
	gone := httptest.NewServer(nil)
	gone.Close()
	targets := []state.PodTarget{{UID: "gone", URL: gone.URL}}
	for _, data := range []string{`{"data":[{"id":"a"},{"id":"b"}]}`, `{"data":[{"id":"b"},{"id":"c"}]}`} {
		pod := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/models" {
				t.Error(r.URL.Path)
			}
			time.Sleep(2 * s.Options.Connect) // Connected; waiting for model discovery headers.
			io.WriteString(w, data)
		}))
		defer pod.Close()
		targets = append(targets, state.PodTarget{UID: pod.URL, URL: pod.URL})
	}
	s.Runtime.Apply(event.Event{Kind: state.EndpointTargets, Key: "ws/ep", Value: targets, ExpiresAt: time.Now().Add(time.Minute)})
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("GET", "/ws/ep/v1/models", nil))
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	for _, id := range []string{"a", "b", "c"} {
		if bytes.Count(rec.Body.Bytes(), []byte(`"id":"`+id+`"`)) != 1 {
			t.Fatal(rec.Body.String())
		}
	}

	s.Runtime.Apply(event.Event{Kind: state.EndpointTargets, Key: "ws/ep", Value: targets[:1], ExpiresAt: time.Now().Add(time.Minute)})
	goneLoad := s.Runtime.View("ws/ep", time.Now()).Pods[0].Load
	if goneLoad.CoolUntil.Load() <= time.Now().UnixNano() {
		t.Fatal("failed dial did not cool down the backend")
	}
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("GET", "/ws/ep/v1/models", nil))
	if rec.Code != 502 {
		t.Fatalf("all pods unavailable: %d", rec.Code)
	}
}

func TestEscapedEndpointForwarding(t *testing.T) {
	const prefix = "/workspace/%77s/external-endpoint/%E6%A8%A1%E5%9E%8B"
	kong := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.EscapedPath(); got != prefix+"/v1/chat/completions" {
			t.Errorf("Kong received path %q", got)
		}
		var req struct{ Temperature float64 }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Temperature != 0.5 {
			t.Errorf("missing decoded route defaults: %v, %v", req, err)
		}
		io.WriteString(w, `{"choices":[]}`)
	}))
	defer kong.Close()
	app := appFor(t, Front, kong.URL)
	err := app.Store.Apply(event.Event{Kind: state.RouteUpsert, Key: "/workspace/ws/external-endpoint/模型", Value: state.Route{ModelDefaults: map[string]map[string]json.RawMessage{"m": {"temperature": json.RawMessage("0.5")}}}})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, post(prefix+"/v1/chat/completions", `{"model":"m"}`))
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}

	pod := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, r.URL.RequestURI()) }))
	defer pod.Close()
	app = appFor(t, Cluster, "")
	app.Runtime.Apply(event.Event{Kind: state.EndpointTargets, Key: "ws/模型", Value: []state.PodTarget{{UID: "pod", URL: pod.URL}}, ExpiresAt: time.Now().Add(time.Minute)})
	for _, tc := range []struct{ suffix, want string }{{"", "/?n=1"}, {"/", "/?n=1"}, {"/a%2Fb", "/a%2Fb?n=1"}} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/%77s/%E6%A8%A1%E5%9E%8B"+tc.suffix+"?n=1", nil)
		app.ServeHTTP(rec, req)
		if rec.Code != 200 || rec.Body.String() != tc.want {
			t.Errorf("root/suffix %q: %d %q", tc.suffix, rec.Code, rec.Body.String())
		}
	}
}

func TestClusterCustomBodies(t *testing.T) {
	const raw = `{"model":"v","messages":{"custom":true}}`
	for _, passthrough := range []bool{true, false} {
		t.Run(fmt.Sprint(passthrough), func(t *testing.T) {
			received := make(chan struct{})
			pod := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var first [1]byte
				_, err := io.ReadFull(r.Body, first[:])
				close(received)
				rest, _ := io.ReadAll(r.Body)
				if err != nil || string(first[:])+string(rest) != raw {
					t.Error("body changed", err)
				}
				io.WriteString(w, "ok")
			}))
			defer pod.Close()
			app := appFor(t, Cluster, "")
			app.Runtime.Apply(event.Event{Kind: state.EndpointTargets, Key: "ws/ep", Value: []state.PodTarget{{UID: "p", URL: pod.URL, Passthrough: passthrough, RoutingLogic: "custom"}}})
			reader, writer := io.Pipe()
			r := httptest.NewRequest("POST", "/ws/ep/v1/chat/completions", reader)
			r.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { defer close(done); app.ServeHTTP(rec, r) }()
			io.WriteString(writer, raw[:1])
			if passthrough {
				select {
				case <-received:
				case <-time.After(time.Second):
					writer.Close()
					<-done
					t.Fatal("router buffered custom upload")
				}
			}
			io.WriteString(writer, raw[1:])
			writer.Close()
			<-done
			if rec.Code != 200 || rec.Body.String() != "ok" {
				t.Fatal(rec.Code, rec.Body.String())
			}
		})
	}
}

func TestMetricsGroupPodSamples(t *testing.T) {
	s := appFor(t, Cluster, "")
	s.Runtime.Apply(event.Event{Kind: state.EndpointTargets, Key: "ws/ep", Value: []state.PodTarget{{UID: "a", Replica: "a", URL: "http://a"}, {UID: "b", Replica: "b", URL: "http://b"}}})
	now := time.Now()
	load := s.Runtime.View("ws/ep", now).Pods[0].Load
	load.Arrive(now)
	load.Finish(now.Add(-2*time.Second), now)
	load.CoolUntil.Store(now.Add(time.Minute).UnixNano())
	load.Waiting.Store(3)
	load.Inflight.Store(4)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	lines := strings.Split(strings.TrimSpace(rec.Body.String()), "\n")
	want := map[string][2]float64{
		"healthy_pods_total": {0, 1}, "current_qps": {1. / 60, 0}, "avg_latency": {2, -1},
		"connection_pool_waiting": {3, 0}, "num_requests_running": {4, 0},
	}
	if len(lines) != 3*len(want) {
		t.Fatal(rec.Body.String())
	}
	for i := 0; i < len(lines); i += 3 {
		h := strings.Fields(lines[i])
		if len(h) != 4 || h[0] != "#" || h[1] != "TYPE" || h[3] != "gauge" {
			t.Fatal(lines[i])
		}
		name := strings.TrimPrefix(h[2], "router:")
		values, ok := want[name]
		if !ok {
			t.Fatal(lines[i])
		}
		delete(want, name)
		for j, replica := range []string{"a", "b"} {
			line := lines[i+j+1]
			series, v, _ := strings.Cut(line, " ")
			value, err := strconv.ParseFloat(v, 64)
			if series != fmt.Sprintf(`router:%s{workspace="ws",endpoint="ep",server="http://%s",replica="%s"}`, name, replica, replica) || err != nil || value != values[j] {
				t.Fatal(line)
			}
		}
	}
}
