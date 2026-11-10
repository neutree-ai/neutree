package dbtest

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// Tests for group policies on one user or one ServiceAccount, written in a
// scope (a Team or an OrgUnit) by whoever manages it there.

const insertSubjectPolicySQL = `
	INSERT INTO api.group_policies (api_version, kind, metadata, spec)
	VALUES ('v1', 'GroupPolicy', ROW(NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL)::api.metadata,
	        ROW($1, $2, $5::jsonb, NULL, $6::jsonb, $3, $4)::api.group_policy_spec)
	RETURNING (metadata).name`

type subjectPolicy struct {
	kind, subject    string
	scopeKind, scope any
	allowed          []map[string]any
	limit            int64
	period           string
}

func (p subjectPolicy) args() []any {
	var allowed, quota any
	if p.allowed != nil {
		allowed = mustJSON(p.allowed)
	}

	if p.limit > 0 {
		period := p.period
		if period == "" {
			period = "monthly"
		}

		quota = mustJSON(map[string]any{"limit": p.limit, "period": period})
	}

	return []any{p.kind, p.subject, p.scopeKind, p.scope, allowed, quota}
}

// setSubjectPolicy stores p as postgres (no RLS; the write checks still run)
// and removes it when the test ends.
func setSubjectPolicy(t *testing.T, p subjectPolicy) string {
	t.Helper()

	db := GetTestDB(t)

	var name string
	if err := db.QueryRow(insertSubjectPolicySQL, p.args()...).Scan(&name); err != nil {
		t.Fatalf("setting %+v failed: %v", p, err)
	}

	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM api.group_policies WHERE (metadata).name = $1`, name)
	})

	return name
}

// writeAs creates p as actor under RLS and rolls back.
func writeAs(t *testing.T, db *sql.DB, actor string, p subjectPolicy) error {
	t.Helper()

	return asUser(t, db, actor, nil, func(tx *sql.Tx) error {
		var name string
		return tx.QueryRow(insertSubjectPolicySQL, p.args()...).Scan(&name)
	})
}

// changeAs updates and deletes the policy name as actor (rolled back) and
// returns the rows each touched.
func changeAs(t *testing.T, db *sql.DB, actor, name string) (updated, deleted int64) {
	t.Helper()

	run := func(query string) int64 {
		var n int64

		if err := asUser(t, db, actor, nil, func(tx *sql.Tx) error {
			res, err := tx.Exec(query, name)
			if err != nil {
				return err
			}

			n, err = res.RowsAffected()

			return err
		}); err != nil {
			t.Fatalf("%s as %s: %v", query, actor, err)
		}

		return n
	}

	return run(`UPDATE api.group_policies SET spec.token_quota = '{"limit": 7}'::jsonb WHERE (metadata).name = $1`),
		run(`DELETE FROM api.group_policies WHERE (metadata).name = $1`)
}

func TestSubjectPolicy_TeamAdminWrites(t *testing.T) {
	f := newSAFixture(t)
	sa := f.mustSA(t)
	outsider := rawUser(t, f.db, "sp-outsider")
	mustExec(t, f.db, `INSERT INTO api.team_members (team_id, user_id) VALUES ($1, $2)`, f.otherID, outsider)

	t.Run("a member and an SA of its Team, with no Team policy above", func(t *testing.T) {
		for _, p := range []subjectPolicy{
			{kind: "User", subject: f.member, scopeKind: "Team", scope: f.team, allowed: gpModels("a"), limit: 100},
			{kind: "ServiceAccount", subject: sa, scopeKind: "Team", scope: f.team, limit: 100},
		} {
			if err := writeAs(t, f.db, f.admin, p); err != nil {
				t.Errorf("%s: %v", p.kind, err)
			}
		}
	})

	t.Run("not in another Team, not globally, not a user outside the Team", func(t *testing.T) {
		err := writeAs(t, f.db, f.admin, subjectPolicy{kind: "User", subject: outsider, scopeKind: "Team", scope: f.other, limit: 100})
		wantErr(t, "a member of another Team", err, "row-level security")

		err = writeAs(t, f.db, f.admin, subjectPolicy{kind: "User", subject: f.member, limit: 100})
		wantErr(t, "a global policy", err, "row-level security")

		err = writeAs(t, f.db, f.admin, subjectPolicy{kind: "User", subject: outsider, scopeKind: "Team", scope: f.team, limit: 100})
		wantErr(t, "a user outside the Team", err, "10329")

		err = writeAs(t, f.db, f.admin, subjectPolicy{kind: "Team", subject: f.team, limit: 100})
		wantErr(t, "the Team's own policy", err, "row-level security")
	})

	t.Run("updates and deletes in its scope only", func(t *testing.T) {
		own := setSubjectPolicy(t, subjectPolicy{kind: "User", subject: f.member, scopeKind: "Team", scope: f.team, limit: 100})
		foreign := setSubjectPolicy(t, subjectPolicy{kind: "User", subject: outsider, scopeKind: "Team", scope: f.other, limit: 100})
		global := setSubjectPolicy(t, subjectPolicy{kind: "User", subject: f.member, limit: 100})

		if u, d := changeAs(t, f.db, f.admin, own); u != 1 || d != 1 {
			t.Errorf("own scope: updated %d, deleted %d", u, d)
		}

		for _, name := range []string{foreign, global} {
			if u, d := changeAs(t, f.db, f.admin, name); u != 0 || d != 0 {
				t.Errorf("%s: updated %d, deleted %d; want none", name, u, d)
			}
		}

		if n := countAs(t, f.db, f.admin, nil, `SELECT count(*) FROM api.group_policies WHERE (metadata).name IN ($1, $2, $3)`,
			own, foreign, global); n != 1 {
			t.Errorf("team-admin reads %d policies, want its scope's 1", n)
		}
	})

	t.Run("others write nothing; the subject reads its own", func(t *testing.T) {
		name := setSubjectPolicy(t, subjectPolicy{kind: "User", subject: f.member, scopeKind: "Team", scope: f.team, limit: 100})

		for _, who := range []string{f.member, f.nobody} {
			err := writeAs(t, f.db, who, subjectPolicy{kind: "ServiceAccount", subject: sa, scopeKind: "Team", scope: f.team, limit: 1})
			wantErr(t, "create", err, "row-level security")

			if u, d := changeAs(t, f.db, who, name); u != 0 || d != 0 {
				t.Errorf("updated %d, deleted %d; want none", u, d)
			}
		}

		if n := countAs(t, f.db, f.member, nil, `SELECT count(*) FROM api.group_policies WHERE (metadata).name = $1`, name); n != 1 {
			t.Errorf("the subject reads %d of its policies, want 1", n)
		}

		if n := countAs(t, f.db, f.nobody, nil, `SELECT count(*) FROM api.group_policies WHERE (metadata).name = $1`, name); n != 0 {
			t.Errorf("another user reads %d, want 0", n)
		}
	})
}

func TestSubjectPolicy_DepartmentAdminWrites(t *testing.T) {
	db := GetTestDB(t)
	o := setupOrgTree(t, db)
	dadmin := o.users["dadmin"]

	t.Run("users of its subtree, in its OrgUnit or one below", func(t *testing.T) {
		for _, c := range []struct{ user, scope string }{{"ua", "a"}, {"ua1", "a"}, {"ua1", "a1"}} {
			if err := writeAs(t, db, dadmin, subjectPolicy{kind: "User", subject: o.users[c.user], scopeKind: "OrgUnit", scope: o.unit(c.scope), limit: 10}); err != nil {
				t.Errorf("%s in %s: %v", c.user, c.scope, err)
			}
		}
	})

	t.Run("not outside its subtree, not above it, not globally", func(t *testing.T) {
		err := writeAs(t, db, dadmin, subjectPolicy{kind: "User", subject: o.users["ub"], scopeKind: "OrgUnit", scope: o.unit("b"), limit: 10})
		wantErr(t, "a user of another OrgUnit", err, "row-level security")

		err = writeAs(t, db, dadmin, subjectPolicy{kind: "User", subject: o.users["ua1"], scopeKind: "OrgUnit", scope: o.unit("root"), limit: 10})
		wantErr(t, "scoped above its OrgUnit", err, "row-level security")

		err = writeAs(t, db, dadmin, subjectPolicy{kind: "User", subject: o.users["ub"], scopeKind: "OrgUnit", scope: o.unit("a"), limit: 10})
		wantErr(t, "a user outside the subtree", err, "10329")

		err = writeAs(t, db, dadmin, subjectPolicy{kind: "User", subject: o.users["ua"], limit: 10})
		wantErr(t, "a global policy", err, "row-level security")
	})

	t.Run("updates and deletes in its subtree only", func(t *testing.T) {
		own := setSubjectPolicy(t, subjectPolicy{kind: "User", subject: o.users["ua1"], scopeKind: "OrgUnit", scope: o.unit("a1"), limit: 10})
		above := setSubjectPolicy(t, subjectPolicy{kind: "User", subject: o.users["ua1"], scopeKind: "OrgUnit", scope: o.unit("root"), limit: 10})

		if u, d := changeAs(t, db, dadmin, own); u != 1 || d != 1 {
			t.Errorf("own subtree: updated %d, deleted %d", u, d)
		}

		if u, d := changeAs(t, db, dadmin, above); u != 0 || d != 0 {
			t.Errorf("above: updated %d, deleted %d; want none", u, d)
		}
	})

	t.Run("a plain member writes nothing", func(t *testing.T) {
		err := writeAs(t, db, o.users["ua"], subjectPolicy{kind: "User", subject: o.users["ua"], scopeKind: "OrgUnit", scope: o.unit("a"), limit: 10})
		wantErr(t, "a member", err, "row-level security")
	})
}

func TestSubjectPolicy_GlobalAdminWrites(t *testing.T) {
	admin := adminToken(t)
	f := newSAFixture(t)
	sa := f.mustSA(t)

	post := func(spec map[string]any) (int, string) {
		return postgrestAs(t, admin, http.MethodPost, "/group_policies", mustJSON(map[string]any{
			"api_version": "v1", "kind": "GroupPolicy", "metadata": map[string]any{}, "spec": spec,
		}))
	}

	code, raw := post(map[string]any{"subject_kind": "User", "subject_name": strings.ToUpper(f.member), "token_quota": map[string]any{"limit": 5}})
	if code != http.StatusCreated {
		t.Fatalf("global user policy = HTTP %d: %s", code, raw)
	}

	var created []struct {
		ID       int
		Metadata struct{ Name string }
		Spec     map[string]any
	}
	if err := json.Unmarshal([]byte(raw), &created); err != nil || len(created) != 1 {
		t.Fatalf("%s: %v", raw, err)
	}

	id := created[0].ID
	t.Cleanup(func() { _, _ = GetTestDB(t).Exec(`DELETE FROM api.group_policies WHERE id = $1`, id) })

	if created[0].Spec["subject_name"] != f.member || created[0].Metadata.Name != "user-"+f.member {
		t.Errorf("stored %s", raw)
	}

	scoped := setSubjectPolicy(t, subjectPolicy{kind: "ServiceAccount", subject: sa, scopeKind: "Team", scope: f.team, limit: 5})

	t.Run("a PATCH without subject or scope keeps them", func(t *testing.T) {
		code, raw := postgrestAs(t, admin, http.MethodPatch, "/group_policies?metadata->>name=eq."+scoped,
			mustJSON(map[string]any{"spec": map[string]any{"token_quota": map[string]any{"limit": 9}}}))
		if code != http.StatusOK || !strings.Contains(raw, `"scope_kind":"Team"`) || !strings.Contains(raw, `"subject_name":"`+sa+`"`) {
			t.Fatalf("HTTP %d: %s", code, raw)
		}
	})

	for _, c := range []struct {
		name string
		spec map[string]any
		code string
	}{
		{"second policy in one scope", map[string]any{"subject_kind": "User", "subject_name": f.member}, "23505"},
		{"not a uuid", map[string]any{"subject_kind": "User", "subject_name": "bob"}, "10294"},
		{"unknown user", map[string]any{"subject_kind": "User", "subject_name": "00000000-0000-0000-0000-000000000001"}, "10294"},
		{"Team policy with a scope", map[string]any{"subject_kind": "Team", "subject_name": f.team, "scope_kind": "Team", "scope_name": f.team}, "10328"},
		{"SA in an OrgUnit", map[string]any{"subject_kind": "ServiceAccount", "subject_name": sa, "scope_kind": "OrgUnit", "scope_name": "x"}, "10328"},
		{"scope without a name", map[string]any{"subject_kind": "User", "subject_name": f.member, "scope_kind": "Team"}, "10328"},
		{"SA in another Team", map[string]any{"subject_kind": "ServiceAccount", "subject_name": sa, "scope_kind": "Team", "scope_name": f.other}, "10329"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if code, raw := post(c.spec); code < 400 || !strings.Contains(raw, c.code) {
				t.Errorf("HTTP %d: %s; want %s", code, raw, c.code)
			}
		})
	}

	t.Run("the scope does not change", func(t *testing.T) {
		code, raw := postgrestAs(t, admin, http.MethodPatch, "/group_policies?metadata->>name=eq."+scoped,
			mustJSON(map[string]any{"spec": map[string]any{"scope_kind": "Team", "scope_name": f.other}}))
		if code != http.StatusBadRequest || !strings.Contains(raw, "10301") {
			t.Errorf("another scope = HTTP %d: %s", code, raw)
		}
	})
}

func TestSubjectPolicy_ModelsIntersect(t *testing.T) {
	db := GetTestDB(t)
	teamA, teamB := newOrgName("sp-a"), newOrgName("sp-b")
	idA, idB := manualTeam(t, db, teamA), manualTeam(t, db, teamB)
	user := rawUser(t, db, "sp-models")
	mustExec(t, db, `INSERT INTO api.team_members (team_id, user_id) VALUES ($1, $3), ($2, $3)`, idA, idB, user)

	t.Run("a subject policy alone limits", func(t *testing.T) {
		setSubjectPolicy(t, subjectPolicy{kind: "User", subject: user, scopeKind: "Team", scope: teamA, allowed: gpModels("x", "y")})

		if got := keyAllowedModels(t, user, gpWorkspace, nil); got != wantModels("x", "y") {
			t.Errorf("got %s", got)
		}
	})

	t.Run("every policy narrows, whoever wrote it", func(t *testing.T) {
		setGroupPolicy(t, "Team", teamA, gpModels("a", "b", "c"), nil)
		setSubjectPolicy(t, subjectPolicy{kind: "User", subject: user, scopeKind: "Team", scope: teamA, allowed: gpModels("x", "y")})
		setSubjectPolicy(t, subjectPolicy{kind: "User", subject: user, allowed: gpModels("b", "c", "d")})
		setSubjectPolicy(t, subjectPolicy{kind: "User", subject: user, scopeKind: "Team", scope: teamB, allowed: gpModels("c", "b", "x")})

		// Team a {a,b,c} ∩ global {b,c,d} ∩ Team b's {b,c,x} ∩ Team a's {x,y} = none.
		if got := keyAllowedModels(t, user, gpWorkspace, nil); got != "[]" {
			t.Errorf("with Team a's {x,y}: got %s, want []", got)
		}

		mustExec(t, db, `DELETE FROM api.group_policies WHERE (spec).subject_name = $1 AND (spec).scope_name = $2`, user, teamA)

		if got := keyAllowedModels(t, user, gpWorkspace, nil); got != wantModels("b", "c") {
			t.Errorf("got %s, want b, c", got)
		}

		own := []map[string]any{{"model": "c", "endpoint_name": "e"}, {"model": "a"}}
		if got := keyAllowedModels(t, user, gpWorkspace, own); got != mustJSON(own[:1]) {
			t.Errorf("a key's own list: got %s", got)
		}
	})

	t.Run("a policy stops applying when the subject leaves its scope", func(t *testing.T) {
		setSubjectPolicy(t, subjectPolicy{kind: "User", subject: user, scopeKind: "Team", scope: teamB, allowed: gpModels("q")})
		mustExec(t, db, `DELETE FROM api.team_members WHERE team_id = $1 AND user_id = $2`, idB, user)

		if got := keyAllowedModels(t, user, gpWorkspace, nil); got != "null" {
			t.Errorf("got %s, want no limit", got)
		}
	})
}

func TestSubjectPolicy_Pools(t *testing.T) {
	f := newSAFixture(t)
	db := f.db
	sa := f.mustSA(t)
	other := newOrgName("sp-pool")
	otherID := manualTeam(t, db, other)
	mustExec(t, db, `INSERT INTO api.team_members (team_id, user_id) VALUES ($1, $2)`, otherID, f.member)

	key1 := createTestAPIKey(t, f.member, saWorkspace, 0)
	key2 := createTestAPIKey(t, f.member, "sp-ws-2", 0)
	saKey := f.mustSAKey(t, sa, nil)

	recordUsage(t, key1, "m1", 20, 10, nil)
	recordUsage(t, key2, "m1", 5, 5, nil)
	recordUsage(t, saKey, "m1", 50, 0, nil)
	aggregateUsage(t)

	t.Run("no pool, no plugin", func(t *testing.T) {
		if got := remaining(t, key1, nil); got != noLimit {
			t.Errorf("remaining %d, want no limit", got)
		}
	})

	setSubjectPolicy(t, subjectPolicy{kind: "User", subject: f.member, scopeKind: "Team", scope: f.team, limit: 100, period: "daily"})

	t.Run("a user's pool counts all its keys", func(t *testing.T) {
		for _, k := range []string{key1, key2} {
			if got := remaining(t, k, nil); got != 60 {
				t.Errorf("remaining %d, want 100 - 40", got)
			}
		}

		var out string
		if err := db.QueryRow(`SELECT api.get_api_key_model_rules($1::uuid, $2, NULL)::text`, f.member, saWorkspace).Scan(&out); err != nil ||
			!strings.Contains(out, `"group_token_pool": true`) {
			t.Errorf("model rules %s, %v; want the quota plugin", out, err)
		}
	})

	t.Run("two Teams setting a pool: the stricter one wins", func(t *testing.T) {
		setSubjectPolicy(t, subjectPolicy{kind: "User", subject: f.member, scopeKind: "Team", scope: other, limit: 30, period: "daily"})

		if got := remaining(t, key2, nil); got != -10 {
			t.Errorf("remaining %d, want 30 - 40", got)
		}
	})

	t.Run("an SA's pool, with its Team's", func(t *testing.T) {
		setSubjectPolicy(t, subjectPolicy{kind: "ServiceAccount", subject: sa, scopeKind: "Team", scope: f.team, limit: 80})

		if got := remaining(t, saKey, nil); got != 30 {
			t.Errorf("remaining %d, want 80 - 50", got)
		}

		setPool(t, "Team", f.team, 100, "monthly")

		if got := remaining(t, saKey, nil); got != 10 {
			t.Errorf("remaining %d, want min(80 - 50, 100 - 40 - 50)", got)
		}
	})
}

func TestSubjectPolicy_Read(t *testing.T) {
	f := newSAFixture(t)
	db := f.db
	sa := f.mustSA(t)
	outsider := rawUser(t, db, "sp-read-out")
	mustExec(t, db, `INSERT INTO api.team_members (team_id, user_id) VALUES ($1, $2)`, f.otherID, outsider)

	setSubjectPolicy(t, subjectPolicy{kind: "User", subject: f.member, scopeKind: "Team", scope: f.team, allowed: gpModels("a"), limit: 100})
	setSubjectPolicy(t, subjectPolicy{kind: "User", subject: f.member, limit: 40})
	setSubjectPolicy(t, subjectPolicy{kind: "ServiceAccount", subject: sa, scopeKind: "Team", scope: f.team, limit: 10})

	policyOf := func(reader, subject string) (map[string]any, error) {
		var raw string

		err := asUser(t, db, reader, nil, func(tx *sql.Tx) error {
			return tx.QueryRow(`SELECT api.get_user_group_policy($1::uuid, $2)::text`, subject, saWorkspace).Scan(&raw)
		})
		if err != nil {
			return nil, err
		}

		var out map[string]any

		return out, json.Unmarshal([]byte(raw), &out)
	}

	t.Run("the subject reads its limits and remaining", func(t *testing.T) {
		got, err := policyOf(f.member, f.member)
		if err != nil {
			t.Fatal(err)
		}

		pools, _ := got["token_pools"].([]any)
		subj, _ := got["subject_policies"].([]any)

		if len(pools) != 2 || len(subj) != 2 || fmt.Sprint(got["allowed_models"]) != "[a]" {
			t.Fatalf("got %v", got)
		}

		// Global first, then by scope.
		if p := pools[1].(map[string]any); p["scope_kind"] != "Team" || p["scope_name"] != f.team || p["remaining"] != float64(100) {
			t.Errorf("Team pool %v", p)
		}
	})

	t.Run("team-admin reads its members and SAs only", func(t *testing.T) {
		for _, subject := range []string{f.member, sa} {
			if _, err := policyOf(f.admin, subject); err != nil {
				t.Errorf("%s: %v", subject, err)
			}
		}

		_, err := policyOf(f.admin, outsider)
		wantErr(t, "a user of another Team", err, "group_policy:read is required")

		_, err = policyOf(f.nobody, f.member)
		wantErr(t, "another user", err, "group_policy:read is required")
	})

	t.Run("a scope's subjects with their policy and effective remaining", func(t *testing.T) {
		type row struct {
			kind, name string
			limit      sql.NullInt64
			effective  sql.NullInt64
			left       sql.NullInt64
			hasID      bool
		}

		read := func(reader, kind, scope string) ([]row, error) {
			var rows []row

			err := asUser(t, db, reader, nil, func(tx *sql.Tx) error {
				rs, err := tx.Query(`SELECT subject_kind, subject_name, (token_quota ->> 'limit')::bigint, effective_remaining,
					(pool ->> 'remaining')::bigint, id IS NOT NULL
					FROM api.get_scope_subject_policies($1, $2)`, kind, scope)
				if err != nil {
					return err
				}
				defer rs.Close()

				for rs.Next() {
					var r row
					if err := rs.Scan(&r.kind, &r.name, &r.limit, &r.effective, &r.left, &r.hasID); err != nil {
						return err
					}

					rows = append(rows, r)
				}

				return rs.Err()
			})

			return rows, err
		}

		rows, err := read(f.admin, "Team", f.team)
		if err != nil {
			t.Fatal(err)
		}

		want := map[string]row{
			f.member: {kind: "User", limit: sql.NullInt64{Int64: 100, Valid: true}, effective: sql.NullInt64{Int64: 40, Valid: true}},
			sa:       {kind: "ServiceAccount", limit: sql.NullInt64{Int64: 10, Valid: true}, effective: sql.NullInt64{Int64: 10, Valid: true}},
		}
		if len(rows) != len(want) {
			t.Fatalf("rows %+v", rows)
		}

		for _, r := range rows {
			w := want[r.name]
			// No usage yet: the pool of the scope's policy is whole.
			if r.kind != w.kind || r.limit != w.limit || r.effective != w.effective || r.left != w.limit || !r.hasID {
				t.Errorf("%s: %+v, want %+v", r.name, r, w)
			}
		}

		_, err = read(f.admin, "Team", f.other)
		wantErr(t, "another Team", err, "group_policy:read on the scope is required")

		_, err = read(f.member, "Team", f.team)
		wantErr(t, "a member", err, "group_policy:read on the scope is required")
	})
}

func TestSubjectPolicy_GoesWithScopeAndSubject(t *testing.T) {
	db := GetTestDB(t)
	team := newOrgName("sp-gone")
	teamID := manualTeam(t, db, team)
	user := rawUser(t, db, "sp-gone")
	mustExec(t, db, `INSERT INTO api.team_members (team_id, user_id) VALUES ($1, $2)`, teamID, user)

	scoped := setSubjectPolicy(t, subjectPolicy{kind: "User", subject: user, scopeKind: "Team", scope: team, limit: 1})
	global := setSubjectPolicy(t, subjectPolicy{kind: "User", subject: user, limit: 1})

	count := func(name string) int {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM api.group_policies WHERE (metadata).name = $1`, name).Scan(&n); err != nil {
			t.Fatal(err)
		}

		return n
	}

	mustExec(t, db, `DELETE FROM api.team_members WHERE team_id = $1`, teamID)
	mustExec(t, db, `DELETE FROM api.teams WHERE id = $1`, teamID)

	if count(scoped) != 0 || count(global) != 1 {
		t.Fatalf("after deleting the Team: scoped %d, global %d; want 0, 1", count(scoped), count(global))
	}

	mustExec(t, db, `DELETE FROM auth.users WHERE id = $1`, user)

	if count(global) != 0 {
		t.Fatal("the user's policy outlived the user")
	}
}

func TestSubjectPolicy_PresetRoles(t *testing.T) {
	db := GetTestDB(t)

	for _, p := range []string{"group_policy:read", "group_policy:create", "group_policy:update", "group_policy:delete"} {
		var teamAdmin bool
		if err := db.QueryRow(`SELECT $1::api.permission_action = ANY((spec).permissions) FROM api.roles WHERE (metadata).name = 'team-admin'`, p).Scan(&teamAdmin); err != nil {
			t.Fatal(err)
		}

		if !teamAdmin {
			t.Errorf("team-admin lacks %s", p)
		}
	}
}
