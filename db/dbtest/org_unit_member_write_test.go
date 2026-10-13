package dbtest

import (
	"context"
	"database/sql"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// Tests for 127_department_admin_assign_member with the default hooks:
// global assigners keep full access to hand-written memberships, and an
// OrgUnit-scoped department-admin gets nothing, since the default subtree
// hook never grants.

func TestOrgUnitMemberWrite_Global(t *testing.T) {
	ctx := context.Background()
	f := newOrgFixture(t)
	db := GetTestDB(t)
	root := f.createOrgUnit("")
	rnd := f.createOrgUnit(root.Metadata.Name)
	ml := f.createOrgUnit(root.Metadata.Name)
	inactive := f.createOrgUnit(root.Metadata.Name)

	if err := f.st.UpdateOrgUnit(inactive.GetID(), &v1.OrgUnit{Status: &v1.OrgStatus{Phase: v1.OrgPhaseInactive}}); err != nil {
		t.Fatalf("deactivating failed: %v", err)
	}

	local := createGoTrueTestUser(t, "oumw-local", "password123")
	other := createGoTrueTestUser(t, "oumw-other", "password123")

	var globalID string
	if err := execWithContext(t, db, nil, func(tx *sql.Tx) error {
		globalID = createUserWithPermissions(t, tx, "oumw-global", "oumw-global@example.com",
			[]string{"org_unit:read", "org_unit:assign-member"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Seeded past the write checks, which reject an OrgUnit scope by default.
	scoped := createGoTrueTestUser(t, "oumw-scoped", "password123")
	if err := execWithContext(t, db, nil, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO api.role_assignments (api_version, kind, metadata, spec)
			VALUES ('v1', 'RoleAssignment',
				ROW($1, NULL, NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
				ROW($2::uuid, NULL, FALSE, 'department-admin', $3)::api.role_assignment_spec)
		`, newOrgName("oumw-scoped"), scoped.ID, root.Metadata.Name)
		return err
	}); err != nil {
		t.Fatalf("seeding the scoped assignment failed: %v", err)
	}

	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM api.role_assignments WHERE (spec).user_id IN ($1::uuid, $2::uuid)`, globalID, scoped.ID)
		_, _ = db.Exec(`DELETE FROM api.roles WHERE (metadata).name = 'oumw-global-role'`)
	})

	exec := func(userID, query string, args ...any) (int64, error) {
		t.Helper()

		var n int64
		err := executeAsUser(t, db, userID, func(tx *sql.Tx) error {
			res, err := tx.ExecContext(ctx, query, args...)
			if err != nil {
				return err
			}
			n, _ = res.RowsAffected()
			return tx.Commit()
		})

		return n, err
	}

	orgUnitOf := func(userID string) int {
		t.Helper()

		var id int
		if err := db.QueryRow(`SELECT org_unit_id FROM api.org_unit_members WHERE user_id = $1::uuid`, userID).Scan(&id); err != nil {
			if err == sql.ErrNoRows {
				return 0
			}
			t.Fatal(err)
		}

		return id
	}

	insert := `INSERT INTO api.org_unit_members (user_id, org_unit_id) VALUES ($1::uuid, $2)`
	move := `UPDATE api.org_unit_members SET org_unit_id = $2 WHERE user_id = $1::uuid`
	remove := `DELETE FROM api.org_unit_members WHERE user_id = $1::uuid`

	t.Run("department-admin holds org_unit:assign-member", func(t *testing.T) {
		var ok bool
		if err := db.QueryRow(`SELECT 'org_unit:assign-member' = ANY((spec).permissions) FROM api.roles WHERE (metadata).name = 'department-admin'`).Scan(&ok); err != nil || !ok {
			t.Fatalf("department-admin lacks org_unit:assign-member: %v, %v", ok, err)
		}
	})

	t.Run("a global assigner inserts, moves and removes", func(t *testing.T) {
		if _, err := exec(globalID, insert, local.ID, rnd.ID); err != nil {
			t.Fatalf("insert failed: %v", err)
		}

		if n, err := exec(globalID, move, local.ID, ml.ID); err != nil || n != 1 || orgUnitOf(local.ID) != ml.ID {
			t.Fatalf("move = %d, %v", n, err)
		}

		if n, err := exec(globalID, `UPDATE api.org_unit_members SET user_id = $2::uuid WHERE user_id = $1::uuid`, local.ID, other.ID); err != nil || n != 1 || orgUnitOf(other.ID) != ml.ID {
			t.Fatalf("changing the user = %d, %v", n, err)
		}

		if n, err := exec(globalID, remove, other.ID); err != nil || n != 1 || orgUnitOf(other.ID) != 0 {
			t.Fatalf("remove = %d, %v", n, err)
		}
	})

	t.Run("a global assigner still cannot use an Inactive OrgUnit", func(t *testing.T) {
		if _, err := exec(globalID, insert, local.ID, rnd.ID); err != nil {
			t.Fatalf("insert failed: %v", err)
		}

		_, err := exec(globalID, move, local.ID, inactive.ID)
		assertErrCode(t, "moving into an Inactive OrgUnit", err, "10277")
	})

	t.Run("an OrgUnit-scoped department-admin writes nothing", func(t *testing.T) {
		_, err := exec(scoped.ID, insert, other.ID, rnd.ID)
		assertErrCode(t, "insert", err, "row-level security")

		if n, err := exec(scoped.ID, move, local.ID, ml.ID); err != nil || n != 0 {
			t.Errorf("move = %d, %v; want no row", n, err)
		}

		if n, err := exec(scoped.ID, remove, local.ID); err != nil || n != 0 {
			t.Errorf("remove = %d, %v; want no row", n, err)
		}

		if got := orgUnitOf(local.ID); got != rnd.ID {
			t.Errorf("membership = %d, want %d", got, rnd.ID)
		}
	})

}
