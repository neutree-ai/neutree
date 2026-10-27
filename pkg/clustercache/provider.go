// Package clustercache defines the cluster cache lifecycle owned by a distribution.
package clustercache

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"

	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// SaveStatus persists operation intent before a provider sends an external request.
type Context struct {
	Cluster         *v1.Cluster
	Client          client.Client
	RESTConfig      *rest.Config
	Namespace       string
	ImagePrefix     string
	ImagePullSecret string
	SaveStatus      func(*v1.ClusterStatus) error
}

// Providers publish their observed status even when reconciliation returns an error.
type Provider interface {
	Validate(old, next *v1.Cluster) error
	Reconcile(context.Context, *Context) error
	Delete(context.Context, *Context) error
}

// MetricsProvider optionally supplies rendered Prometheus scrape_configs entries.
// The provider owns service discovery and labels; the metrics component embeds
// these trusted entries without interpreting provider-specific configuration.
type MetricsProvider interface {
	MetricsScrapeConfig(cluster *v1.Cluster, namespace string) string
}

// Providers is an instance-owned registry. Its zero value is ready for registration.
// Pass it by pointer; registration must not copy the mutex or expose the map.
type Providers struct {
	mu        sync.RWMutex
	providers map[string]Provider
}

func (p *Providers) Register(name string, provider Provider) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("cluster cache provider name must not be empty")
	}

	if provider == nil {
		return fmt.Errorf("cluster cache provider %q must not be nil", name)
	}

	// An interface containing a nil pointer is not itself nil.
	value := reflect.ValueOf(provider)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if value.IsNil() {
			return fmt.Errorf("cluster cache provider %q must not be nil", name)
		}
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if _, exists := p.providers[name]; exists {
		return fmt.Errorf("cluster cache provider %q is already registered", name)
	}

	if p.providers == nil {
		p.providers = make(map[string]Provider)
	}

	p.providers[name] = provider

	return nil
}

func (p *Providers) Get(name string) (Provider, error) {
	if p != nil {
		p.mu.RLock()
		provider, ok := p.providers[name]
		p.mu.RUnlock()

		if ok {
			return provider, nil
		}
	}

	return nil, fmt.Errorf("cluster cache provider %q is not supported", name)
}

func (p *Providers) Names() []string {
	names := []string{}

	if p != nil {
		p.mu.RLock()
		for name := range p.providers {
			names = append(names, name)
		}
		p.mu.RUnlock()
	}

	sort.Strings(names)

	return names
}

func (p *Providers) Validate(old, next *v1.Cluster) error {
	var desired, installed string

	if next != nil && next.Spec != nil && next.Spec.Cache != nil {
		desired = next.Spec.Cache.Provider
	}

	if old != nil && old.Status != nil && old.Status.Cache != nil {
		installed = old.Status.Cache.Provider
	}

	if desired != "" && installed != "" && desired != installed {
		return fmt.Errorf("remove the existing cluster cache and wait for cleanup before changing provider")
	}

	name := desired

	if name == "" {
		name = installed
	}

	if name == "" {
		return nil
	}

	provider, err := p.Get(name)

	if err != nil {
		return err
	}

	return provider.Validate(old, next)
}
