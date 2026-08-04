package admission

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
)

const (
	communityOrderMin  = 0
	communityOrderMax  = 999
	enterpriseOrderMin = 1000
	enterpriseOrderMax = 1999
	enterprisePrefix   = "enterprise."
)

// Registry collects resource descriptors and hooks during startup.
type Registry struct {
	mu        sync.RWMutex
	resources map[string]*resourceHooks
	codes     map[int]HookMeta
	sealed    bool
}

type resourceHooks struct {
	objectType reflect.Type
	hooks      map[Operation][]Hook
	byName     map[hookKey]struct{}
}

type hookKey struct {
	operation Operation
	phase     Phase
	name      string
}

type resourceNamer interface {
	admissionResourceDescriptor() resourceDescriptor
}

type resourceDescriptor struct {
	Name       string
	ObjectType reflect.Type
}

// Chain is an immutable, ordered operation-specific admission chain.
type Chain struct {
	hooks []Hook
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		resources: make(map[string]*resourceHooks),
		codes:     make(map[int]HookMeta),
	}
}

// RegisterResource registers a REST write resource, including resources with
// no hooks. Hooks also create a resource descriptor when first registered.
func (r *Registry) RegisterResource(resource any) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.sealed {
		return fmt.Errorf("admission registry is sealed")
	}
	descriptor, err := resolveResourceDescriptor(resource)
	if err != nil {
		return err
	}
	if descriptor.Name == "" {
		return fmt.Errorf("admission resource name is empty")
	}
	if existing, exists := r.resources[descriptor.Name]; exists {
		if err := validateDescriptorType(descriptor.Name, existing.objectType, descriptor.ObjectType); err != nil {
			return err
		}
		return fmt.Errorf("admission resource %q is already registered", descriptor.Name)
	}
	r.resources[descriptor.Name] = newResourceHooks(descriptor.ObjectType)
	return nil
}

// RegisterHook registers a typed hook against an existing resource.
func (r *Registry) RegisterHook(resource any, hook Hook) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.sealed {
		return fmt.Errorf("admission registry is sealed")
	}
	descriptor, err := resolveResourceDescriptor(resource)
	if err != nil {
		return err
	}
	if descriptor.Name == "" {
		return fmt.Errorf("admission resource name is empty")
	}
	if err := validateHook(hook); err != nil {
		return err
	}
	resourceHooks, exists := r.resources[descriptor.Name]
	if !exists {
		resourceHooks = newResourceHooks(descriptor.ObjectType)
		r.resources[descriptor.Name] = resourceHooks
	} else if err := validateDescriptorType(descriptor.Name, resourceHooks.objectType, descriptor.ObjectType); err != nil {
		return err
	}
	if resourceHooks.objectType == nil && descriptor.ObjectType != nil {
		resourceHooks.objectType = descriptor.ObjectType
	}
	key := hookKey{operation: hook.meta.Operation, phase: hook.meta.Phase, name: hook.meta.Name}
	if _, exists := resourceHooks.byName[key]; exists {
		return fmt.Errorf("admission hook %q is already registered for resource %q", hook.meta.Name, descriptor.Name)
	}
	if existing, exists := r.codes[hook.code]; exists {
		return fmt.Errorf("admission error code %d is already registered by hook %q", hook.code, existing.Name)
	}
	if hook.meta.Phase == Mutating && hasMutatorOrder(resourceHooks.hooks[hook.meta.Operation], hook.meta.Order) {
		return fmt.Errorf("admission mutator order %d is already registered for resource %q", hook.meta.Order, descriptor.Name)
	}

	resourceHooks.hooks[hook.meta.Operation] = append(resourceHooks.hooks[hook.meta.Operation], hook)
	resourceHooks.byName[key] = struct{}{}
	r.codes[hook.code] = hook.meta
	return nil
}

// Seal validates and freezes all registrations. It is idempotent.
func (r *Registry) Seal() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.sealed {
		return nil
	}
	for _, resourceHooks := range r.resources {
		for operation, hooks := range resourceHooks.hooks {
			sort.Slice(hooks, func(i, j int) bool {
				left, right := hooks[i].meta, hooks[j].meta
				if left.Phase != right.Phase {
					return left.Phase < right.Phase
				}
				if left.Order != right.Order {
					return left.Order < right.Order
				}
				return left.Name < right.Name
			})
			resourceHooks.hooks[operation] = hooks
		}
	}
	r.sealed = true
	return nil
}

// Chain returns the immutable operation chain for a registered resource.
func (r *Registry) Chain(resource any, operation Operation) (*Chain, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if !r.sealed {
		return nil, fmt.Errorf("admission registry is not sealed")
	}
	descriptor, err := resolveResourceDescriptor(resource)
	if err != nil {
		return nil, err
	}
	resourceHooks, exists := r.resources[descriptor.Name]
	if !exists {
		return nil, fmt.Errorf("admission resource %q is not registered", descriptor.Name)
	}
	if err := validateDescriptorType(descriptor.Name, resourceHooks.objectType, descriptor.ObjectType); err != nil {
		return nil, err
	}
	if !validOperation(operation) {
		return nil, fmt.Errorf("invalid admission operation %d", operation)
	}
	hooks := append([]Hook(nil), resourceHooks.hooks[operation]...)
	return &Chain{hooks: hooks}, nil
}

// Hooks returns the ordered metadata for the chain's hooks.
func (c *Chain) Hooks() []HookMeta {
	metadata := make([]HookMeta, len(c.hooks))
	for i, hook := range c.hooks {
		metadata[i] = hook.meta
	}
	return metadata
}

// Mutate runs the chain's mutation hooks in order and returns the final candidate.
func (c *Chain) Mutate(ctx RequestContext, candidate any) (any, error) {
	current := candidate
	for _, hook := range c.hooks {
		if hook.meta.Phase != Mutating {
			continue
		}
		if err := contextError(ctx); err != nil {
			return nil, err
		}
		next, err := hook.mutate(ctx, current)
		if err != nil {
			return nil, err
		}
		current = next
	}
	return current, nil
}

// Validate runs the chain's validation hooks in order. Validators receive JSON
// round-trip copies of the supplied objects through their typed constructors.
func (c *Chain) Validate(ctx RequestContext, old, candidate any) error {
	for _, hook := range c.hooks {
		if hook.meta.Phase != Validating {
			continue
		}
		if err := contextError(ctx); err != nil {
			return err
		}
		if err := hook.validate(ctx, old, candidate); err != nil {
			return err
		}
	}
	return nil
}

// Run executes the full chain in phase order.
func (c *Chain) Run(ctx RequestContext, old, candidate any) (any, error) {
	mutated, err := c.Mutate(ctx, candidate)
	if err != nil {
		return nil, err
	}
	if err := c.Validate(ctx, old, mutated); err != nil {
		return nil, err
	}
	return mutated, nil
}

func validateHook(hook Hook) error {
	if hook.meta.Name == "" {
		return fmt.Errorf("admission hook name is empty")
	}
	if !validOperation(hook.meta.Operation) {
		return fmt.Errorf("invalid admission operation %d", hook.meta.Operation)
	}
	if !validPhase(hook.meta.Phase) {
		return fmt.Errorf("invalid admission phase %d", hook.meta.Phase)
	}
	if hook.meta.Phase == Mutating && hook.mutate == nil {
		return fmt.Errorf("admission mutator %q has no function", hook.meta.Name)
	}
	if hook.meta.Phase == Validating && hook.validate == nil {
		return fmt.Errorf("admission validator %q has no function", hook.meta.Name)
	}
	if hook.meta.Operation == Update && hook.meta.Phase == Mutating {
		return fmt.Errorf("update mutation hook %q is not supported", hook.meta.Name)
	}
	if hook.code <= 0 {
		return fmt.Errorf("admission hook %q has invalid error code %d", hook.meta.Name, hook.code)
	}
	if err := validateOrderBand(hook.meta); err != nil {
		return err
	}
	return nil
}

func validateOrderBand(meta HookMeta) error {
	if strings.HasPrefix(meta.Name, enterprisePrefix) {
		if meta.Order < enterpriseOrderMin || meta.Order > enterpriseOrderMax {
			return fmt.Errorf("enterprise admission hook %q order %d is outside %d-%d", meta.Name, meta.Order, enterpriseOrderMin, enterpriseOrderMax)
		}
		return nil
	}
	if meta.Order < communityOrderMin || meta.Order > communityOrderMax {
		return fmt.Errorf("community admission hook %q order %d is outside %d-%d", meta.Name, meta.Order, communityOrderMin, communityOrderMax)
	}
	return nil
}

func hasMutatorOrder(hooks []Hook, order int) bool {
	for _, hook := range hooks {
		if hook.meta.Phase == Mutating && hook.meta.Order == order {
			return true
		}
	}
	return false
}

func validOperation(operation Operation) bool {
	return operation == Create || operation == Update || operation == Delete
}

func validPhase(phase Phase) bool {
	return phase == Mutating || phase == Validating
}

func resolveResourceDescriptor(resource any) (resourceDescriptor, error) {
	switch value := resource.(type) {
	case string:
		return resourceDescriptor{Name: value}, nil
	case resourceNamer:
		return value.admissionResourceDescriptor(), nil
	default:
		return resourceDescriptor{}, fmt.Errorf("unsupported admission resource type %T", resource)
	}
}

func contextError(ctx RequestContext) error {
	if ctx.Context == nil {
		return nil
	}
	return ctx.Context.Err()
}

func newResourceHooks(objectType reflect.Type) *resourceHooks {
	return &resourceHooks{
		objectType: objectType,
		hooks:      make(map[Operation][]Hook),
		byName:     make(map[hookKey]struct{}),
	}
}

func validateDescriptorType(name string, existing, requested reflect.Type) error {
	if requested == nil {
		return nil
	}
	if existing == nil {
		return nil
	}
	if existing != requested {
		return fmt.Errorf("admission resource %q is registered for %s, not %s", name, existing, requested)
	}
	return nil
}
