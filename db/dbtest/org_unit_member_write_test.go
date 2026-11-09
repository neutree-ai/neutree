package dbtest

import (
	"context"
	"database/sql"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// Tests for org_unit:assign-member: global assigners keep full
// access to hand-written memberships. The subtree side is in
// TestOrgUnitMemberWrite_Subtree.

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

	scoped := createGoTrueTestUser(t, "oumw-scoped", "password123")
	seedRoleAssignment(t, db, roleAssignment{UserID: scoped.ID, Role: "department-admin", OrgUnit: root.Metadata.Name, Replica: true})

	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM api.role_assignments WHERE (spec).user_id = $1::uuid`, globalID)
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
}
