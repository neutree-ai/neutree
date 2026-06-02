package strategy

import (
	"strings"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestGet_UnknownStrategy(t *testing.T) {
	_, err := Get("nonexistent")
	if err == nil {
		t.Fatalf("expected error for unknown strategy")
	}
}

func TestGet_DefaultStrategy(t *testing.T) {
	s, err := Get("standard")
	if err != nil {
		t.Fatalf("Get(standard): %v", err)
	}
	got, err := Get("")
	if err != nil {
		t.Fatalf("Get(empty): %v", err)
	}
	if got != s {
		t.Fatalf("empty strategy should resolve to standard")
	}
}

func TestStandard_Validate_AllowsSingleRoleOrNoRole(t *testing.T) {
	s, err := Get("standard")
	if err != nil {
		t.Fatalf("Get(standard): %v", err)
	}
	cases := []*v1.Endpoint{
		{Spec: &v1.EndpointSpec{}},
		{Spec: &v1.EndpointSpec{Roles: []v1.EndpointRoleSpec{{Name: "engine"}}}},
	}
	for _, ep := range cases {
		if err := s.Validate(ep); err != nil {
			t.Fatalf("Validate(%+v): %v", ep.Spec.Roles, err)
		}
	}
}

func TestStandard_Validate_RejectsKVTransfer(t *testing.T) {
	s, err := Get("standard")
	if err != nil {
		t.Fatalf("Get(standard): %v", err)
	}
	err = s.Validate(&v1.Endpoint{Spec: &v1.EndpointSpec{
		Strategy: "standard",
		KV:       &v1.KVSpec{Transfer: &v1.KVTransferSpec{Connector: "nixl"}},
	}})
	if err == nil {
		t.Fatalf("expected standard strategy to reject kv.transfer")
	}
	if got := err.Error(); !strings.Contains(got, "kv.transfer") {
		t.Errorf("error %q does not mention kv.transfer", got)
	}
}

func TestPD_Validate_Failures(t *testing.T) {
	s, _ := Get("pd")
	tests := []struct {
		name    string
		ep      *v1.Endpoint
		wantSub string
	}{
		{
			name: "missing_prefill",
			ep: &v1.Endpoint{Spec: &v1.EndpointSpec{
				Placement: &v1.PlacementSpec{Roles: "same-host"},
				Roles:     []v1.EndpointRoleSpec{{Name: "decode"}},
			}},
			wantSub: "prefill and decode",
		},
		{
			name: "unsupported_placement",
			ep: &v1.Endpoint{Spec: &v1.EndpointSpec{
				Placement: &v1.PlacementSpec{Roles: "spread-host"},
			}},
			wantSub: "same-host",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := s.Validate(tc.ep)
			if err == nil {
				t.Fatalf("expected error")
			}
			if got := err.Error(); !strings.Contains(got, tc.wantSub) {
				t.Errorf("error %q does not contain %q", got, tc.wantSub)
			}
		})
	}
}

func TestPD_Validate_RolesSameHost1P1D(t *testing.T) {
	s, _ := Get("pd")
	if err := s.Validate(&v1.Endpoint{
		Spec: &v1.EndpointSpec{
			Strategy:  "pd",
			Placement: &v1.PlacementSpec{Roles: "same-host"},
			Roles: []v1.EndpointRoleSpec{
				{Name: "prefill"},
				{Name: "decode"},
			},
		},
	}); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestPD_Validate_DefaultsPlacementRoles(t *testing.T) {
	s, _ := Get("pd")
	if err := s.Validate(&v1.Endpoint{
		Spec: &v1.EndpointSpec{
			Strategy: "pd",
			Roles: []v1.EndpointRoleSpec{
				{Name: "prefill"},
				{Name: "decode"},
			},
		},
	}); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}
