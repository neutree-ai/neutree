package dbtest

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Fixture for the OrgUnit subtree tests (org_subtree_*_test.go): role
// bindings scoped to an OrgUnit, delegation and what they unlock.
//
// The tree:
//
//	root
//	├── a            <- dadmin is department-admin here (and a member)
//	│   ├── a1
//	│   └── x        (Inactive)
//	└── b
//
// Every check runs in its own transaction that is rolled back, so one check
// never sees another's writes.

type orgTree struct {
	suffix string
	source string
	units  map[string]int // short name -> id
	users  map[string]string
}

func (o *orgTree) unit(short string) string { return short + "-" + o.suffix }

const smallRole = "neu839-small"

func setupOrgTree(t *testing.T, db *sql.DB) *orgTree {
	t.Helper()

	o := &orgTree{
		suffix: fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000_000),
		units:  map[string]int{},
		users:  map[string]string{},
	}
	o.source = "neu839-src-" + o.suffix

	// The identity source row only has to exist (OrgUnits name it); its write
	// checks are not under test here.
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	mustTxExec(t, tx, `SET LOCAL session_replication_role = replica`)
	mustTxExec(t, tx, `
		INSERT INTO api.identity_sources (api_version, kind, metadata)
		VALUES ('v1', 'IdentitySource', ROW($1, NULL, NULL, NULL, now(), now(), NULL, NULL)::api.metadata)`, o.source)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	for _, n := range []struct{ short, parent string }{
		{"root", ""}, {"a", "root"}, {"a1", "a"}, {"x", "a"}, {"b", "root"},
	} {
		parent := ""
		if n.parent != "" {
			parent = o.unit(n.parent)
		}

		var id int
		if err := db.QueryRow(`
			INSERT INTO api.org_units (api_version, kind, metadata, spec, path)
			VALUES ('v1', 'OrgUnit', ROW($1, NULL, NULL, NULL, NULL, NULL, NULL, NULL)::api.metadata,
			        ROW($2, $3, $4)::api.org_unit_spec, '/0/')
			RETURNING id`, o.unit(n.short), o.source, "ext-"+o.unit(n.short), parent).Scan(&id); err != nil {
			t.Fatalf("creating OrgUnit %s failed: %v", n.short, err)
		}
		o.units[n.short] = id
	}

	for i, n := range []struct{ short, unit string }{
		{"dadmin", "a"}, {"ua", "a"}, {"ua1", "a1"}, {"ux", "x"}, {"ub", "b"}, {"nobody", ""},
	} {
		id := fmt.Sprintf("839%05d-0000-0000-0000-%012d", i, time.Now().UnixNano()%1_000_000_000_000)
		name := "neu839-" + n.short + "-" + o.suffix
		mustExec(t, db, `
			INSERT INTO auth.users (id, instance_id, aud, role, email, encrypted_password,
			                        raw_user_meta_data, created_at, updated_at)
			VALUES ($1, '00000000-0000-0000-0000-000000000000', 'authenticated', 'authenticated',
			        $2, 'x', jsonb_build_object('username', $3::text), now(), now())`,
			id, name+"@test.local", name)
		o.users[n.short] = id

		if n.unit != "" {
			mustExec(t, db, `INSERT INTO api.org_unit_members (user_id, org_unit_id, identity_source) VALUES ($1, $2, $3)`,
				id, o.units[n.unit], o.source)
		}
	}

	// x goes Inactive after its member was put in.
	mustExec(t, db, `UPDATE api.org_units SET status = ROW('Inactive', NULL)::api.org_status WHERE id = $1`, o.units["x"])

	mustExec(t, db, `
		INSERT INTO api.roles (api_version, kind, metadata, spec)
		SELECT 'v1', 'Role', ROW($1, NULL, NULL, NULL, now(), now(), NULL, NULL)::api.metadata,
		       ROW(NULL, ARRAY['user_profile:read']::api.permission_action[])::api.role_spec
		WHERE NOT EXISTS (SELECT 1 FROM api.roles WHERE (metadata).name = $1)`, smallRole)

	o.bind(t, db, "dadmin", "department-admin", "a")

	t.Cleanup(func() {
		ids := []string{}
		for _, id := range o.users {
			ids = append(ids, "'"+id+"'")
		}
		in := strings.Join(ids, ",")
		_, _ = db.Exec(`DELETE FROM api.role_assignments WHERE (spec).user_id IN (` + in + `)`)
		_, _ = db.Exec(`DELETE FROM auth.users WHERE id IN (` + in + `)`)
		for _, short := range []string{"a1", "x", "a", "b", "root"} {
			_, _ = db.Exec(`DELETE FROM api.org_units WHERE id = $1`, o.units[short])
		}
		_, _ = db.Exec(`DELETE FROM api.identity_sources WHERE (metadata).name = $1`, o.source)
	})

	return o
}

// bind stores an OrgUnit-scoped assignment as postgres (no actor, so the
// delegation check does not apply).
func (o *orgTree) bind(t *testing.T, db *sql.DB, user, role, unit string) {
	t.Helper()

	seedRoleAssignment(t, db, roleAssignment{
		Name:   fmt.Sprintf("neu839-%s-%s-%s-%s", user, role, unit, o.suffix),
		UserID: o.users[user], Role: role, OrgUnit: o.unit(unit),
	})
}

func mustTxExec(t *testing.T, tx *sql.Tx, query string, args ...any) {
	t.Helper()

	if _, err := tx.Exec(query, args...); err != nil {
		t.Fatalf("%s failed: %v", query, err)
	}
}

// asUser runs prelude as postgres, then fn as userID through RLS (the
// PostgREST >= 10 claims shape), and rolls everything back unless fn commits.
func asUser(t *testing.T, db *sql.DB, userID string, prelude func(tx *sql.Tx), fn func(tx *sql.Tx) error) error {
	t.Helper()

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()

	if prelude != nil {
		prelude(tx)
	}

	claims, _ := json.Marshal(map[string]any{"sub": userID, "role": "api_user"})
	mustTxExec(t, tx, `SET LOCAL ROLE api_user`)
	mustTxExec(t, tx, `SELECT set_config('request.jwt.claims', $1, true)`, string(claims))
	mustTxExec(t, tx, `SET LOCAL app.settings.jwt_secret = 'test'`)

	return fn(tx)
}

func countAs(t *testing.T, db *sql.DB, userID string, prelude func(tx *sql.Tx), query string, args ...any) int {
	t.Helper()

	var n int
	if err := asUser(t, db, userID, prelude, func(tx *sql.Tx) error {
		return tx.QueryRow(query, args...).Scan(&n)
	}); err != nil {
		t.Fatalf("%s failed: %v", query, err)
	}

	return n
}

func wantErr(t *testing.T, what string, err error, contains string) {
	t.Helper()

	if err == nil {
		t.Fatalf("%s succeeded, want an error with %q", what, contains)
	}
	if !strings.Contains(err.Error(), contains) {
		t.Fatalf("%s failed with %v, want %q", what, err, contains)
	}
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()

	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("setup statement failed: %v", err)
	}
}

// manualTeam stores a Team created in Neutree as postgres and returns its id.
func manualTeam(t *testing.T, db *sql.DB, name string) int {
	t.Helper()

	var id int
	if err := db.QueryRow(`
		INSERT INTO api.teams (api_version, kind, metadata, spec)
		VALUES ('v1', 'Team', ROW($1, NULL, NULL, NULL, NULL, NULL, NULL, NULL)::api.metadata, ROW(NULL, NULL)::api.team_spec)
		RETURNING id`, name).Scan(&id); err != nil {
		t.Fatalf("creating Team %s failed: %v", name, err)
	}

	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM api.role_assignments WHERE (spec).subject_kind = 'Team' AND (spec).subject_name = $1`, name)
		_, _ = db.Exec(`DELETE FROM api.teams WHERE id = $1`, id)
	})

	return id
}

// groupBinding returns a prelude that stores a group assignment as postgres.
// global is FALSE when orgUnit is set.
func groupBinding(t *testing.T, kind, subject, role, orgUnit string) func(tx *sql.Tx) {
	return func(tx *sql.Tx) {
		ra := roleAssignment{Global: orgUnit == "", Role: role, OrgUnit: orgUnit, SubjectKind: kind, SubjectName: subject}
		if _, err := insertRoleAssignment(tx, ra); err != nil {
			t.Fatalf("storing %+v failed: %v", ra, err)
		}
	}
}

func checkIn(t *testing.T, db *sql.DB, prelude func(tx *sql.Tx), query string, args ...any) bool {
	t.Helper()

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()

	if prelude != nil {
		prelude(tx)
	}

	var ok bool
	if err := tx.QueryRow(query, args...).Scan(&ok); err != nil {
		t.Fatalf("%s failed: %v", query, err)
	}

	return ok
}
