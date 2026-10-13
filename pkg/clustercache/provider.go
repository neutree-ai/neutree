// Package clustercache defines the cluster cache lifecycle owned by a distribution.
package clustercache

import (
	"context"
	"fmt"
	"sort"

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

type Providers map[string]Provider

func (p Providers) Get(name string) (Provider, error) {
	provider, ok := p[name]
	if !ok {
		return nil, fmt.Errorf("cluster cache provider %q is not supported", name)
	}

	return provider, nil
}

func (p Providers) Names() []string {
	names := make([]string, 0, len(p))
	for name := range p {
		names = append(names, name)
	}

	sort.Strings(names)

	return names
}

func (p Providers) Validate(old, next *v1.Cluster) error {
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
