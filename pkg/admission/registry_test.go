package admission_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/neutree-ai/neutree/pkg/admission"
)

const testResource = "widgets"

type widget struct {
	Name   string
	Labels map[string]string
}

type otherWidget struct{}

var widgetResource = admission.NewResource[widget](testResource)

func TestRegistryOrdersHooksByPhaseOrderAndName(t *testing.T) {
	registry := admission.NewRegistry()
	require.NoError(t, registry.RegisterResource(widgetResource))
	require.NoError(t, registry.RegisterHook(widgetResource, admission.ValidateCreate(
		admission.HookMeta{Name: "community.z-last", Order: 20}, 10003, validateWidget,
	)))
	require.NoError(t, registry.RegisterHook(widgetResource, admission.ValidateCreate(
		admission.HookMeta{Name: "community.a-first", Order: 20}, 10001, validateWidget,
	)))
	require.NoError(t, registry.RegisterHook(widgetResource, admission.MutateCreate(
		admission.HookMeta{Name: "community.mutate", Order: 40}, 10002, mutateWidget,
	)))
	require.NoError(t, registry.Seal())

	chain, err := registry.Chain(widgetResource, admission.Create)
	require.NoError(t, err)
	require.Equal(t, []admission.HookMeta{
		{Name: "community.mutate", Operation: admission.Create, Phase: admission.Mutating, Order: 40},
		{Name: "community.a-first", Operation: admission.Create, Phase: admission.Validating, Order: 20},
		{Name: "community.z-last", Operation: admission.Create, Phase: admission.Validating, Order: 20},
	}, chain.Hooks())
}

func TestRegistryRejectsDuplicateHookMetadata(t *testing.T) {
	registry := newWidgetRegistry(t)
	hook := admission.ValidateCreate(admission.HookMeta{Name: "community.duplicate", Order: 1}, 10001, validateWidget)
	require.NoError(t, registry.RegisterHook(widgetResource, hook))
	require.Error(t, registry.RegisterHook(widgetResource, hook))
}

func TestRegistryRegistersResourceWhenHookIsRegistered(t *testing.T) {
	registry := admission.NewRegistry()
	unregistered := admission.NewResource[widget]("unregistered")
	require.NoError(t, registry.RegisterHook(unregistered, admission.ValidateCreate(
		admission.HookMeta{Name: "community.unregistered", Order: 1}, 10001, validateWidget,
	)))
	require.NoError(t, registry.Seal())
	chain, err := registry.Chain(unregistered, admission.Create)
	require.NoError(t, err)
	require.Len(t, chain.Hooks(), 1)
}

func TestRegistryRejectsDifferentDescriptorTypeForSameResourceName(t *testing.T) {
	registry := admission.NewRegistry()
	first := admission.NewResource[widget]("same")
	second := admission.NewResource[otherWidget]("same")
	require.NoError(t, registry.RegisterHook(first, admission.ValidateCreate(
		admission.HookMeta{Name: "community.widget", Order: 1}, 10001, validateWidget,
	)))
	err := registry.RegisterHook(second, admission.ValidateCreate(
		admission.HookMeta{Name: "community.other-widget", Order: 2}, 10002,
		func(_ admission.RequestContext, _ otherWidget) error { return nil },
	))
	require.ErrorContains(t, err, "registered for")
	require.NoError(t, registry.Seal())
	_, err = registry.Chain(second, admission.Create)
	require.ErrorContains(t, err, "registered for")
}

func TestRegistryBindsTypedHookToEmptyResourceDescriptor(t *testing.T) {
	registry := admission.NewRegistry()
	first := admission.NewResource[widget]("same")
	second := admission.NewResource[otherWidget]("same")
	require.NoError(t, registry.RegisterResource("same"))
	require.NoError(t, registry.RegisterHook(first, admission.ValidateCreate(
		admission.HookMeta{Name: "community.widget", Order: 1}, 10001, validateWidget,
	)))
	err := registry.RegisterHook(second, admission.ValidateCreate(
		admission.HookMeta{Name: "community.other-widget", Order: 2}, 10002,
		func(_ admission.RequestContext, _ otherWidget) error { return nil },
	))
	require.ErrorContains(t, err, "registered for")
}

func TestRegistryBindsStringHookRegistrationToItsHookType(t *testing.T) {
	registry := admission.NewRegistry()
	second := admission.NewResource[otherWidget]("same")
	require.NoError(t, registry.RegisterHook("same", admission.ValidateCreate(
		admission.HookMeta{Name: "community.widget", Order: 1}, 10001, validateWidget,
	)))
	err := registry.RegisterHook(second, admission.ValidateCreate(
		admission.HookMeta{Name: "community.other-widget", Order: 2}, 10002,
		func(_ admission.RequestContext, _ otherWidget) error { return nil },
	))
	require.ErrorContains(t, err, "registered for")
}

func TestRegistryRejectsDuplicateMutatorOrder(t *testing.T) {
	registry := newWidgetRegistry(t)
	require.NoError(t, registry.RegisterHook(widgetResource, admission.MutateCreate(
		admission.HookMeta{Name: "community.first", Order: 1}, 10001, mutateWidget,
	)))
	require.Error(t, registry.RegisterHook(widgetResource, admission.MutateCreate(
		admission.HookMeta{Name: "community.second", Order: 1}, 10002, mutateWidget,
	)))
}

func TestRegistryValidatesCommunityAndEnterpriseOrderBands(t *testing.T) {
	tests := []struct {
		name      string
		hook      admission.Hook
		wantError bool
	}{
		{
			name: "community hook in community band",
			hook: admission.ValidateCreate(admission.HookMeta{Name: "community.valid", Order: 999}, 10001, validateWidget),
		},
		{
			name:      "community hook in enterprise band",
			hook:      admission.ValidateCreate(admission.HookMeta{Name: "community.invalid", Order: 1000}, 10001, validateWidget),
			wantError: true,
		},
		{
			name: "enterprise hook in enterprise band",
			hook: admission.ValidateCreate(admission.HookMeta{Name: "enterprise.valid", Order: 1000}, 21100, validateWidget),
		},
		{
			name:      "enterprise hook in community band",
			hook:      admission.ValidateCreate(admission.HookMeta{Name: "enterprise.invalid", Order: 999}, 21100, validateWidget),
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry := newWidgetRegistry(t)
			err := registry.RegisterHook(widgetResource, tt.hook)
			if tt.wantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestRegistryRejectsMutateUpdateInFirstRelease(t *testing.T) {
	registry := newWidgetRegistry(t)
	require.Error(t, registry.RegisterHook(widgetResource, admission.MutateUpdate(
		admission.HookMeta{Name: "community.update", Order: 1}, 10001, mutateWidget,
	)))
}

func TestRegistryIsImmutableAfterSeal(t *testing.T) {
	registry := newWidgetRegistry(t)
	require.NoError(t, registry.Seal())
	require.Error(t, registry.RegisterResource("other"))
	require.Error(t, registry.RegisterHook(widgetResource, admission.ValidateCreate(
		admission.HookMeta{Name: "community.after-seal", Order: 1}, 10001, validateWidget,
	)))
}

func TestRegistryRejectsDuplicateNumericCode(t *testing.T) {
	registry := newWidgetRegistry(t)
	require.NoError(t, registry.RegisterHook(widgetResource, admission.ValidateCreate(
		admission.HookMeta{Name: "community.first", Order: 1}, 10001, validateWidget,
	)))
	require.Error(t, registry.RegisterHook(widgetResource, admission.ValidateCreate(
		admission.HookMeta{Name: "community.second", Order: 2}, 10001, validateWidget,
	)))
}

func TestRegistryChainStopsWhenRequestContextIsCanceled(t *testing.T) {
	registry := newWidgetRegistry(t)
	ran := false
	require.NoError(t, registry.RegisterHook(widgetResource, admission.ValidateCreate(
		admission.HookMeta{Name: "community.cancellation", Order: 1}, 10001,
		func(_ admission.RequestContext, _ widget) error {
			ran = true
			return nil
		},
	)))
	require.NoError(t, registry.Seal())
	chain, err := registry.Chain(widgetResource, admission.Create)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = chain.Run(admission.RequestContext{Context: ctx}, nil, widget{})
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, ran)
}

func TestRegistryChainRunsTypedHooksWithSnapshotIsolation(t *testing.T) {
	registry := newWidgetRegistry(t)
	validated := false
	require.NoError(t, registry.RegisterHook(widgetResource, admission.MutateCreate(
		admission.HookMeta{Name: "community.mutate", Order: 1}, 10001,
		func(_ admission.RequestContext, candidate widget) (widget, error) {
			return widget{Name: candidate.Name + "-mutated", Labels: map[string]string{"from": "mutator"}}, nil
		},
	)))
	require.NoError(t, registry.RegisterHook(widgetResource, admission.ValidateCreate(
		admission.HookMeta{Name: "community.validate", Order: 2}, 10002,
		func(_ admission.RequestContext, candidate widget) error {
			validated = true
			candidate.Labels["validator"] = "must not leak"
			return nil
		},
	)))
	require.NoError(t, registry.Seal())
	chain, err := registry.Chain(widgetResource, admission.Create)
	require.NoError(t, err)

	result, err := chain.Run(admission.RequestContext{}, nil, widget{Name: "input"})
	require.NoError(t, err)
	require.True(t, validated)
	require.Equal(t, widget{Name: "input-mutated", Labels: map[string]string{"from": "mutator"}}, result)
}

func TestRegistryChainPassesSnapshotsToUpdateAndDeleteValidators(t *testing.T) {
	registry := newWidgetRegistry(t)
	updated := false
	deleted := false
	require.NoError(t, registry.RegisterHook(widgetResource, admission.ValidateUpdate(
		admission.HookMeta{Name: "community.update", Order: 1}, 10001,
		func(_ admission.RequestContext, old, candidate widget) error {
			updated = old.Name == "old" && candidate.Name == "updated"
			old.Labels["validator"] = "must not leak"
			candidate.Labels["validator"] = "must not leak"
			return nil
		},
	)))
	require.NoError(t, registry.RegisterHook(widgetResource, admission.ValidateDelete(
		admission.HookMeta{Name: "community.delete", Order: 1}, 10002,
		func(_ admission.RequestContext, old, candidate widget) error {
			deleted = old.Name == "old" && candidate.Name == "deleted"
			return nil
		},
	)))
	require.NoError(t, registry.Seal())

	old := widget{Name: "old", Labels: map[string]string{"original": "old"}}
	updatedCandidate := widget{Name: "updated", Labels: map[string]string{"original": "updated"}}
	updateChain, err := registry.Chain(widgetResource, admission.Update)
	require.NoError(t, err)
	require.NoError(t, updateChain.Validate(admission.RequestContext{}, old, updatedCandidate))
	require.True(t, updated)
	require.Equal(t, map[string]string{"original": "old"}, old.Labels)
	require.Equal(t, map[string]string{"original": "updated"}, updatedCandidate.Labels)

	deleteChain, err := registry.Chain(widgetResource, admission.Delete)
	require.NoError(t, err)
	require.NoError(t, deleteChain.Validate(admission.RequestContext{}, old, widget{Name: "deleted"}))
	require.True(t, deleted)
}

func TestErrorPreservesExpectedRejectionDetails(t *testing.T) {
	var nilError *admission.Error
	withoutHint := &admission.Error{Code: 10001, Message: "rejected"}
	withHint := &admission.Error{Code: 10002, Message: "rejected", Hint: "fix input"}
	require.Equal(t, "", nilError.Error())
	require.EqualError(t, withoutHint, "admission rejection 10001: rejected")
	require.EqualError(t, withHint, "admission rejection 10002: rejected (hint: fix input)")
	require.Error(t, errors.Join(withoutHint))
}

func newWidgetRegistry(t *testing.T) *admission.Registry {
	t.Helper()
	registry := admission.NewRegistry()
	require.NoError(t, registry.RegisterResource(widgetResource))
	return registry
}

func mutateWidget(_ admission.RequestContext, candidate widget) (widget, error) {
	return candidate, nil
}

func validateWidget(_ admission.RequestContext, _ widget) error {
	return nil
}
