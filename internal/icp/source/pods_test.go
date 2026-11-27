package source

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/neutree-ai/neutree/internal/icp/event"
	"github.com/neutree-ai/neutree/internal/icp/state"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	typed "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
)

type inspectPodRequest func(*http.Request) (*http.Response, error)

func (f inspectPodRequest) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestEmptyListThenPodWatch(t *testing.T) {
	watching := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	timeout := time.After(3 * time.Second)
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("labelSelector") != "app=inference" {
			t.Error("selector omitted")
		}
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		if r.URL.Query().Get("watch") != "true" {
			enc.Encode(core.PodList{TypeMeta: meta.TypeMeta{Kind: "PodList", APIVersion: "v1"}, ListMeta: meta.ListMeta{ResourceVersion: "7"}})
			return
		}
		if r.URL.Query().Get("resourceVersion") != "7" {
			t.Error("watch did not resume at list revision")
		}
		close(watching)
		p := core.Pod{TypeMeta: meta.TypeMeta{Kind: "Pod", APIVersion: "v1"}, ObjectMeta: meta.ObjectMeta{Name: "replica", UID: "uid", Labels: map[string]string{"workspace": "ws", "endpoint": "ep", "routing_logic": "kv_aware", "passthrough": "true"}}, Status: core.PodStatus{PodIP: "10.1.2.3", Conditions: []core.PodCondition{{Type: core.PodReady, Status: core.ConditionTrue}}}}
		for i, op := range []string{"ADDED", "MODIFIED", "MODIFIED"} {
			if i == 1 {
				p.Spec.Containers = []core.Container{{Ports: []core.ContainerPort{{ContainerPort: 9090}}}}
			}
			if i == 2 {
				now := meta.Now()
				p.DeletionTimestamp = &now
			}
			enc.Encode(map[string]any{"type": op, "object": p})
			w.(http.Flusher).Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()
	client, err := typed.NewForConfig(&rest.Config{Host: server.URL, WrapTransport: func(rt http.RoundTripper) http.RoundTripper {
		return inspectPodRequest(func(r *http.Request) (*http.Response, error) {
			_, hasDeadline := r.Context().Deadline()
			watch := r.URL.Query().Has("watch")
			if hasDeadline == watch {
				t.Errorf("watch=%t, request deadline=%t", watch, hasDeadline)
			}
			return rt.RoundTrip(r)
		})
	}})
	if err != nil {
		t.Fatal(err)
	}
	src := K8sPodSource{Pods: client.Pods("ns"), Selector: "app=inference"}
	events := make(chan event.Event, 3)
	done := make(chan error, 1)
	go func() { done <- src.Run(ctx, func(_ context.Context, e event.Event) error { events <- e; return nil }) }()
	defer func() { cancel(); <-done }()
	select {
	case <-watching:
	case <-timeout:
		t.Fatal("timed out waiting for pod watch")
	}
	if !src.Ready.Load() {
		t.Fatal("empty initial list not ready")
	}
	for _, wantURL := range []string{"http://10.1.2.3:8000", "http://10.1.2.3:9090", ""} {
		select {
		case e := <-events:
			ps := e.Value.([]state.PodTarget)
			if e.Key != "ws/ep" || !e.ExpiresAt.IsZero() {
				t.Fatal(e.Key, e.ExpiresAt)
			}
			if wantURL == "" {
				if len(ps) != 0 {
					t.Fatal("terminating pod retained", ps)
				}
			} else if len(ps) != 1 || ps[0].URL != wantURL || ps[0].RoutingLogic != "kv_aware" || !ps[0].Passthrough {
				t.Fatalf("want %s, got %v", wantURL, ps)
			}
		case <-timeout:
			t.Fatal("timed out waiting for pod watch")
		}
	}
}
