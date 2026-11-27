package source

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	typed "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/neutree-ai/neutree/internal/icp/event"
	"github.com/neutree-ai/neutree/internal/icp/state"
)

type K8sPodSource struct {
	Pods     typed.PodInterface
	Selector string
	Ready    atomic.Bool
}

func NewInCluster(namespace, selector string) (*K8sPodSource, error) {
	var cfg *rest.Config
	var err error

	if path := os.Getenv("KUBECONFIG"); path != "" {
		cfg, err = clientcmd.BuildConfigFromFlags("", path)
	} else {
		cfg, err = rest.InClusterConfig()
	}

	if err != nil {
		return nil, err
	}

	client, err := typed.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}

	return &K8sPodSource{Pods: client.Pods(namespace), Selector: selector}, nil
}

func (*K8sPodSource) Name() string { return "k8s-pods" }

type podIndex struct {
	groups map[string]map[string]state.PodTarget
	owner  map[string]string
}

func (idx *podIndex) put(key string, p state.PodTarget) {
	if idx.groups[key] == nil {
		idx.groups[key] = make(map[string]state.PodTarget)
	}

	idx.groups[key][p.UID] = p
	idx.owner[p.UID] = key
}

func target(p *core.Pod) (string, state.PodTarget, bool) {
	key := p.Labels["workspace"] + "/" + p.Labels["endpoint"]
	if p.Labels["workspace"] == "" || p.Labels["endpoint"] == "" || p.DeletionTimestamp != nil || p.Status.PodIP == "" {
		return key, state.PodTarget{}, false
	}

	for _, c := range p.Status.Conditions {
		if c.Type == core.PodReady && c.Status == core.ConditionTrue {
			logic := p.Labels["routing_logic"]
			if logic == "" {
				logic = "roundrobin"
			}

			port := "8000"

			if len(p.Spec.Containers) > 0 {
				ports := p.Spec.Containers[0].Ports
				if len(ports) > 0 && ports[0].ContainerPort > 0 {
					port = strconv.Itoa(int(ports[0].ContainerPort))
				}
			}

			return key, state.PodTarget{
				UID: string(p.UID), Replica: p.Name, URL: "http://" + net.JoinHostPort(p.Status.PodIP, port),
				RoutingLogic: logic, Passthrough: p.Labels["passthrough"] == "true",
			}, true
		}
	}

	return key, state.PodTarget{}, false
}

func (s *K8sPodSource) Run(ctx context.Context, emit event.Emit) error {
	idx := podIndex{owner: make(map[string]string)}
	publish := func(key string) error {
		group := idx.groups[key]
		pods := make([]state.PodTarget, 0, len(group))

		for _, p := range group {
			pods = append(pods, p)
		}

		return emit(ctx, event.Event{Kind: state.EndpointTargets, Lane: event.LaneRuntime, Key: key, Value: pods})
	}

	syncOnce := func() error {
		listCtx, cancel := context.WithTimeout(ctx, 30*time.Second)

		list, err := s.Pods.List(listCtx, meta.ListOptions{LabelSelector: s.Selector})

		cancel()

		if err != nil {
			return err
		}

		old := idx.groups
		idx.groups = make(map[string]map[string]state.PodTarget)

		clear(idx.owner)

		for i := range list.Items {
			if k, p, ok := target(&list.Items[i]); ok {
				idx.put(k, p)
			}
		}

		for k := range old {
			if _, ok := idx.groups[k]; !ok {
				if err := publish(k); err != nil {
					return err
				}
			}
		}

		for k := range idx.groups {
			if err := publish(k); err != nil {
				return err
			}
		}

		s.Ready.Store(true)

		stream, err := s.Pods.Watch(ctx, meta.ListOptions{ResourceVersion: list.ResourceVersion, LabelSelector: s.Selector})
		if err != nil {
			return err
		}

		defer stream.Stop()

		for {
			select {
			case <-ctx.Done():
				return nil
			case e, ok := <-stream.ResultChan():
				if !ok {
					return nil
				}

				if err := idx.apply(e, publish); err != nil {
					return err
				}
			}
		}
	}
	for ctx.Err() == nil {
		if err := syncOnce(); err != nil && ctx.Err() == nil {
			slog.Warn("pod discovery will relist", "error", err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}

	return ctx.Err()
}

func (idx *podIndex) apply(e watch.Event, publish func(string) error) error {
	if e.Type == watch.Error {
		return fmt.Errorf("pod watch: %v", e.Object)
	}

	pod, ok := e.Object.(*core.Pod)
	if !ok {
		return nil
	}

	uid := string(pod.UID)
	key, p, ready := target(pod)

	if prev, found := idx.owner[uid]; found && (e.Type == watch.Deleted || !ready || key != prev) {
		delete(idx.groups[prev], uid)
		delete(idx.owner, uid)

		if err := publish(prev); err != nil {
			return err
		}
	}

	if e.Type != watch.Deleted && ready {
		idx.put(key, p)

		return publish(key)
	}

	return nil
}
