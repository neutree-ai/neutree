package dbtest

import (
	"context"
	"database/sql"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// Tests for 126_has_permission_in_any_scope with the default hooks: only a
// global assignment counts; an OrgUnit-scoped one never does.

func TestHasPermissionInAnyScope(t *testing.T) {
	ctx := context.Background()
	f := newOrgFixture(t)
	db := GetTestDB(t)

	active := f.createOrgUnit("")
	inactive := f.createOrgUnit("")
	if err := f.st.UpdateOrgUnit(inactive.GetID(), &v1.OrgUnit{Status: &v1.OrgStatus{Phase: v1.OrgPhaseInactive}}); err != nil {
		t.Fatalf("marking the OrgUnit Inactive failed: %v", err)
	}

	var globalID string
	if err := execWithContext(t, db, nil, func(tx *sql.Tx) error {
		globalID = createUserWithPermissions(t, tx, "hpias-global", "hpias-global@example.com", []string{"org_unit:read"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	none := createGoTrueTestUser(t, "hpias-none", "password123")
	onActive := createGoTrueTestUser(t, "hpias-active", "password123")
	onInactive := createGoTrueTestUser(t, "hpias-inactive", "password123")

	seedOrgScopedAssignment(t, db, onActive.ID, "department-admin", active.Metadata.Name)
	seedOrgScopedAssignment(t, db, onInactive.ID, "department-admin", inactive.Metadata.Name)

	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM api.role_assignments WHERE (spec).user_id IN ($1::uuid, $2::uuid, $3::uuid)`,
			globalID, onActive.ID, onInactive.ID)
		_, _ = db.Exec(`DELETE FROM api.roles WHERE (metadata).name = 'hpias-global-role'`)
	})

	cases := []struct {
		name string
		user string
		perm string
		want bool
	}{
		{"global assignment with the permission", globalID, "org_unit:read", true},
		{"global assignment without the permission", globalID, "user_profile:read", false},
		{"no assignment", none.ID, "org_unit:read", false},
		{"subtree assignment on an Active OrgUnit", onActive.ID, "org_unit:read", false},
		{"subtree assignment on an Inactive OrgUnit", onInactive.ID, "org_unit:read", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Run as api_user, as PostgREST does for /rpc.
			var got bool
			if err := executeAsUser(t, db, c.user, func(tx *sql.Tx) error {
				return tx.QueryRowContext(ctx, `SELECT api.has_permission_in_any_scope($1::uuid, $2::api.permission_action)`,
					c.user, c.perm).Scan(&got)
			}); err != nil {
				t.Fatalf("has_permission_in_any_scope failed: %v", err)
			}
			if got != c.want {
				t.Fatalf("has_permission_in_any_scope(%s) = %v, want %v", c.perm, got, c.want)
			}
		})
	}
}
