package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/neutree-ai/neutree/internal/icp/event"
	"github.com/neutree-ai/neutree/internal/icp/state"
)

const (
	iePath      = "/workspace/ws/endpoint/ep"
	eePath      = "/workspace/ws/external-endpoint/provider"
	chatSuffix  = "/v1/chat/completions"
	audioSuffix = "/v1/audio/transcriptions"
	wireReply   = `{"id":"result","model":"served","choices":[{"message":{"role":"assistant","content":"answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`
)

type chainRecord struct {
	Request  struct{ ID, URI string }
	Consumer struct {
		ID string `json:"custom_id"`
	}
	AI struct {
		Trace *struct {
			Upstream      string
			UpstreamModel string `json:"upstream_model"`
		}
		Statistics *struct {
			Usage struct {
				Total int `json:"total_tokens"`
			}
		}
	}
}

func TestGatewayChain(t *testing.T) {
	open := func(h http.Handler) *httptest.Server {
		s := httptest.NewServer(h)
		t.Cleanup(s.Close)
		return s
	}
	cluster := appFor(t, Cluster, "")
	cluster.Options.Idle = 2 * time.Second
	var pods []state.PodTarget
	var visits [2]atomic.Int32
	var failed atomic.Bool
	for i := range visits {
		node := open(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			visits[i].Add(1)
			for key := range r.Header {
				assert.False(t, strings.HasPrefix(strings.ToLower(key), strings.ToLower(internal)), key)
			}
			w.Header().Set("X-Replica", strconv.Itoa(i))
			w.Header().Set(internal+"Consumer-Id", "untrusted")
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Query().Has("retry") && failed.CompareAndSwap(false, true) {
				w.WriteHeader(502)
				return
			}
			if r.URL.Path == audioSuffix {
				reader, err := r.MultipartReader()
				if !assert.NoError(t, err) {
					return
				}
				var fields []string
				for {
					p, err := reader.NextPart()
					if err == io.EOF {
						break
					}
					if !assert.NoError(t, err) {
						return
					}
					fields = append(fields, p.FormName())
					n, err := io.Copy(io.Discard, p)
					assert.NoError(t, err)
					if p.FormName() == "file" {
						assert.EqualValues(t, 1<<20, n)
					}
				}
				assert.Equal(t, []string{"model", "file"}, fields)
				io.WriteString(w, wireReply)
				return
			}
			assert.Equal(t, chatSuffix, r.URL.Path)
			var req struct {
				Model  string
				Stream bool
			}
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&req)) {
				return
			}
			if r.URL.Query().Has("external") {
				assert.Equal(t, "served", req.Model)
			} else {
				assert.Equal(t, "alias", req.Model)
			}
			if !req.Stream {
				io.WriteString(w, wireReply)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: "+`{"id":"up","model":"served","choices":[{"delta":{"content":"answer"}}]}`+"\n\n")
			w.(http.Flusher).Flush()
			switch {
			case r.URL.Query().Has("cut"):
				panic(http.ErrAbortHandler)
			case r.URL.Query().Has("idle"):
				<-r.Context().Done()
			default:
				io.WriteString(w, "data: "+`{"choices":[],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`+"\n\ndata: [DONE]\n\n")
			}
		}))
		id := strconv.Itoa(i)
		pods = append(pods, state.PodTarget{UID: id, Replica: id, URL: node.URL})
	}
	setPolicy := func(policy string) {
		set := append([]state.PodTarget(nil), pods...)
		for i := range set {
			set[i].RoutingLogic = policy
		}
		cluster.Runtime.Apply(event.Event{Kind: state.EndpointTargets, Key: "ws/ep", Value: set})
	}
	setPolicy("roundrobin")
	router := open(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Empty(t, r.Header.Get(internal+"Token"))
		if which := r.URL.Query().Get("sticky"); which != "" {
			if which == "prefix" {
				assert.Empty(t, r.Header.Get(internal+"Session"))
			} else {
				assert.Equal(t, "affinity", r.Header.Get(internal+"Session"))
			}
			assert.NotEmpty(t, r.Header.Get(internal+"Prefix-Hash"))
		}
		cluster.ServeHTTP(w, r)
	}))
	var kongCalls atomic.Int32
	kong := open(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		kongCalls.Add(1)
		if r.URL.Query().Has("legacy") {
			assert.Empty(t, r.Header.Get(internal+"Unpacked"))
			assert.Empty(t, r.Header.Get(internal+"Token"))
			io.Copy(w, r.Body)
			return
		}
		assert.Equal(t, "1", r.Header.Get(internal+"Unpacked"))
		assert.Equal(t, "alias", r.Header.Get(internal+"Model"))
		if r.Header.Get(internal+"Token") != "secret" {
			http.Error(w, "invalid token", 401)
			return
		}
		status := r.URL.Query().Get("deny")
		if status != "" {
			code, err := strconv.Atoi(status)
			assert.NoError(t, err)
			if code != 401 {
				w.Header().Set(internal+"Consumer-Id", "key")
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			io.WriteString(w, `{"message":"policy denied"}`)
			return
		}
		external := strings.HasPrefix(r.URL.Path, eePath+"/")
		base := router.URL
		path := "/ws/ep" + strings.TrimPrefix(r.URL.Path, iePath)
		if external {
			base, path = pods[0].URL, strings.TrimPrefix(r.URL.Path, eePath)
			var obj map[string]json.RawMessage
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&obj)) {
				return
			}
			obj["model"] = json.RawMessage(`"served"`)
			b, err := json.Marshal(obj)
			if !assert.NoError(t, err) {
				return
			}
			r.Body, r.ContentLength = io.NopCloser(bytes.NewReader(b)), int64(len(b))
		}
		u, err := url.Parse(base)
		if !assert.NoError(t, err) {
			return
		}
		p := httputil.NewSingleHostReverseProxy(u)
		r.URL.Path = path
		affinity := []string{"Prefix-Hash", "Block-Hashes", "Session"}
		headers := r.Header.Clone()
		cleanInternal(r.Header)
		if !external {
			for _, key := range affinity {
				if v := headers.Get(internal + key); v != "" {
					r.Header.Set(internal+key, v)
				}
			}
		}
		p.ModifyResponse = func(resp *http.Response) error {
			cleanInternal(resp.Header)
			resp.Header.Set(internal+"Consumer-Id", "key")
			if external {
				resp.Header.Set(internal+"Selected-Upstream", "provider")
				resp.Header.Set(internal+"Upstream-Model", "served")
			}
			return nil
		}
		p.ServeHTTP(w, r)
	}))

	records := make(chan chainRecord, 32)
	vector := open(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rec chainRecord
		if assert.NoError(t, json.NewDecoder(r.Body).Decode(&rec)) {
			records <- rec
		}
	}))
	front := appFor(t, Front, kong.URL)
	front.Options.Token = "secret"
	front.Options.Idle = 350 * time.Millisecond
	front.Sink = NewVectorSink(vector.URL, 32)
	client := open(front)
	l0 := appFor(t, L0, kong.URL)
	l0.Sink = front.Sink
	legacy := open(l0)
	defer func() {
		require.NoError(t, front.Sink.Drain(context.Background()))
		require.Empty(t, records, "unexpected extra Vector record")
	}()
	do := func(path, media string, body io.Reader, status int) (string, string) {
		t.Helper()
		target := client.URL
		if strings.Contains(path, "legacy") {
			target = legacy.URL
		}
		req, err := http.NewRequest(http.MethodPost, target+path, body)
		require.NoError(t, err)
		req.Header.Set("Content-Type", media)
		if req.URL.Query().Get("sticky") != "prefix" {
			req.Header.Set("X-Session-ID", "affinity")
		}
		req.Header.Set(internal+"Token", "client supplied")
		before := kongCalls.Load()
		resp, err := client.Client().Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, status, resp.StatusCode, string(b))
		require.Equal(t, before+1, kongCalls.Load())
		require.Empty(t, resp.Header.Get(internal+"Consumer-Id"))
		if target == client.URL {
			select {
			case rec := <-records:
				require.NotEmpty(t, rec.Request.ID)
				require.NotNil(t, rec.AI.Trace)
				require.Equal(t, path, rec.Request.URI)
				if status == 401 {
					require.Empty(t, rec.Consumer.ID)
				} else {
					require.Equal(t, "key", rec.Consumer.ID)
				}
				if status != 200 || strings.Contains(path, "cut") || strings.Contains(path, "idle") {
					require.Nil(t, rec.AI.Statistics)
				} else {
					require.NotNil(t, rec.AI.Statistics)
					require.Equal(t, 3, rec.AI.Statistics.Usage.Total)
				}
				if strings.Contains(path, "external") {
					require.Equal(t, "provider", rec.AI.Trace.Upstream)
					require.Equal(t, "served", rec.AI.Trace.UpstreamModel)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("missing Vector POST", path)
			}
		}
		return string(b), resp.Header.Get("X-Replica")
	}
	const jsonMedia = "application/json"
	for _, tc := range []struct{ path, inputs, terminal string }{
		{chatSuffix, `"messages":[{"role":"user","content":"hi"}]`, "data: [DONE]"},
		{"/anthropic/v1/messages", `"messages":[{"role":"user","content":"hi"}]`, "event: message_stop"},
		{"/v1/responses", `"input":"hi"`, "event: response.completed"},
	} {
		for _, stream := range []bool{false, true} {
			body := fmt.Sprintf(`{"model":"alias",%s,"stream":%t}`, tc.inputs, stream)
			b, _ := do(iePath+tc.path, jsonMedia, strings.NewReader(body), 200)
			require.Contains(t, b, "answer")
			if stream {
				require.Contains(t, b, tc.terminal)
			} else if tc.path == chatSuffix {
				require.Equal(t, wireReply, b)
			}
		}
	}
	const chatBody = `{ "model":"alias", "messages":[{"role":"user","content":"hi"}] }`
	for i, status := range []int{401, 403, 429} {
		before := visits[0].Load() + visits[1].Load()
		path := []string{chatSuffix, "/anthropic/v1/messages", "/v1/responses"}[i]
		body := chatBody
		if i == 2 {
			body = `{"model":"alias","input":"hi"}`
		}
		b, _ := do(iePath+path+"?deny="+strconv.Itoa(status), jsonMedia, strings.NewReader(body), status)
		require.Contains(t, b, `"error":{`)
		require.Contains(t, b, "policy denied")
		if i == 1 {
			require.Contains(t, b, `"type":"error"`)
		}
		require.Equal(t, before, visits[0].Load()+visits[1].Load())
	}
	var upload bytes.Buffer
	form := multipart.NewWriter(&upload)
	require.NoError(t, form.WriteField("model", "alias"))
	file, err := form.CreateFormFile("file", "large.wav")
	require.NoError(t, err)
	_, err = file.Write(bytes.Repeat([]byte("x"), 1<<20))
	require.NoError(t, err)
	require.NoError(t, form.Close())
	denied, _ := do(iePath+audioSuffix+"?deny=403", form.FormDataContentType(), bytes.NewReader(upload.Bytes()), 403)
	require.JSONEq(t, `{"error":{"type":"permission_error","message":"policy denied"}}`, denied)
	do(iePath+audioSuffix, form.FormDataContentType(), &upload, 200)
	do(eePath+chatSuffix+"?external=1", jsonMedia, strings.NewReader(chatBody), 200)
	setPolicy("consistent_hash")
	for _, affinity := range []string{"session", "prefix"} {
		path := iePath + chatSuffix + "?sticky=" + affinity
		_, a := do(path, jsonMedia, strings.NewReader(chatBody), 200)
		_, b := do(path, jsonMedia, strings.NewReader(chatBody), 200)
		require.NotEmpty(t, a)
		require.Equal(t, a, b)
	}
	first, second := visits[0].Load(), visits[1].Load()
	do(iePath+chatSuffix+"?retry=1", jsonMedia, strings.NewReader(chatBody), 200)
	require.Equal(t, first+1, visits[0].Load())
	require.Equal(t, second+1, visits[1].Load())
	for _, fault := range []string{"cut", "idle"} {
		before := visits[0].Load() + visits[1].Load()
		buf, _ := do(iePath+"/v1/responses?"+fault+"=1", jsonMedia, strings.NewReader(`{"model":"alias","input":"hi","stream":true}`), 200)
		require.Contains(t, buf, "answer")
		require.Contains(t, buf, "event: error")
		require.NotContains(t, buf, "event: response.completed")
		require.Equal(t, before+1, visits[0].Load()+visits[1].Load())
	}
	raw, _ := do(iePath+chatSuffix+"?legacy=1", jsonMedia, strings.NewReader(chatBody), 200)
	require.Equal(t, chatBody, raw)
}
