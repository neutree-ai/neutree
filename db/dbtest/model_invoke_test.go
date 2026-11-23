package dbtest

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"
)

// Tests for model:invoke: calling models is granted apart from reading
// endpoints.

// The migration gives model:invoke to every role that could call models
// before; re-running it in a rolled-back transaction checks the backfill on
// roles created after it ran.
func TestModelInvoke_Backfill(t *testing.T) {
	db := GetTestDB(t)

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()

	roles := map[string]string{
		"endpoint-reader":          "endpoint:read",
		"external-endpoint-reader": "external_endpoint:read",
		"cluster-reader":           "cluster:read",
	}
	names := map[string]string{}

	for key, perm := range roles {
		names[key] = newOrgName("mi-" + key)
		mustTxExec(t, tx, `
			INSERT INTO api.roles (api_version, kind, metadata, spec)
			VALUES ('v1', 'Role', ROW($1, NULL, NULL, NULL, now(), now(), NULL, NULL)::api.metadata,
			        ROW(NULL, ARRAY[$2]::api.permission_action[])::api.role_spec)`, names[key], perm)
	}

	mustTxExec(t, tx, readMigration(t, "134_model_invoke.up.sql"))

	cases := []struct {
		role       string
		wantInvoke bool
	}{
		{"endpoint-reader", true},
		{"external-endpoint-reader", true},
		{"cluster-reader", false},
	}

	for _, tc := range cases {
		got := strings.Contains(rolePermissions(t, tx, names[tc.role]), "model:invoke")
		if got != tc.wantInvoke {
			t.Errorf("%s: holds model:invoke = %v, want %v", tc.role, got, tc.wantInvoke)
		}
	}

	if perms := rolePermissions(t, tx, "workspace-user"); !strings.Contains(perms, "model:invoke") {
		t.Errorf("workspace-user lacks model:invoke: %s", perms)
	}
}

func TestModelInvoke_InvokeWithoutEndpointRead(t *testing.T) {
	db := GetTestDB(t)
	ws := newOrgName("mi-ws")
	insertServedModelsEndpoint(t, db, newOrgName("mi-ep"), ws, servedEndpointStatus("mi-model"), false)

	invoker := rawUser(t, db, "mi-invoker")
	bindGlobal(t, db, invoker, customRole(t, db, "model:invoke"))

	reader := rawUser(t, db, "mi-reader")
	bindGlobal(t, db, reader, customRole(t, db, "endpoint:read"))

	nobody := rawUser(t, db, "mi-nobody")

	t.Run("model:invoke alone sees no endpoint", func(t *testing.T) {
		if n := countAs(t, db, invoker, nil, `SELECT count(*) FROM api.endpoints WHERE (metadata).workspace = $1`, ws); n != 0 {
			t.Fatalf("invoker sees %d endpoints, want 0", n)
		}

		if n := countAs(t, db, reader, nil, `SELECT count(*) FROM api.endpoints WHERE (metadata).workspace = $1`, ws); n != 1 {
			t.Fatalf("endpoint reader sees %d endpoints, want 1", n)
		}
	})

	t.Run("model:invoke lists the models of the workspace with their phase", func(t *testing.T) {
		var model, typ, phase string
		if err := asUser(t, db, invoker, nil, func(tx *sql.Tx) error {
			return tx.QueryRow(`SELECT model, type, phase FROM api.list_invokable_models($1)`, ws).Scan(&model, &typ, &phase)
		}); err != nil {
			t.Fatalf("list_invokable_models failed: %v", err)
		}

		if model != "mi-model" || typ != "internal" || phase != "Running" {
			t.Fatalf("got (%s, %s, %s), want (mi-model, internal, Running)", model, typ, phase)
		}
	})

	t.Run("without model:invoke the list is empty", func(t *testing.T) {
		for name, user := range map[string]string{"endpoint:read only": reader, "no role": nobody} {
			if got := invokableModels(t, db, user, ws); len(got) != 0 {
				t.Errorf("%s: listed %v, want none", name, got)
			}
		}
	})

	t.Run("the gateway check follows model:invoke", func(t *testing.T) {
		if !hasPermission(t, invoker, "model:invoke", ws) {
			t.Error("invoker: has_permission(model:invoke) = false")
		}

		if hasPermission(t, reader, "model:invoke", ws) {
			t.Error("a role created with endpoint:read only grants model:invoke")
		}
	})
}

// The model-invoker preset is what a person who only calls models gets: the
// workspace to create a personal key in, the key in the key list, and still
// no endpoint.
func TestModelInvoke_PresetSeesWorkspaceAndOwnKeys(t *testing.T) {
	db := GetTestDB(t)
	invoker := rawUser(t, db, "mi-preset")
	bindGlobal(t, db, invoker, "model-invoker")

	if n := countAs(t, db, invoker, nil, `SELECT count(*) FROM api.workspaces WHERE (metadata).name = 'default'`); n != 1 {
		t.Fatalf("model-invoker sees %d default workspaces, want 1", n)
	}

	if n := countAs(t, db, invoker, nil, `SELECT count(*) FROM api.endpoints`); n != 0 {
		t.Fatalf("model-invoker sees %d endpoints, want 0", n)
	}

	err := asUser(t, db, invoker, nil, func(tx *sql.Tx) error {
		var id string
		if err := tx.QueryRow(`SELECT id FROM api.create_api_key(p_workspace := 'default', p_name := 'mi-preset-key', p_quota := 0)`).Scan(&id); err != nil {
			return err
		}

		var n int
		if err := tx.QueryRow(`
			SELECT count(*) FROM api.get_api_key_project_groups(NULL, NULL, NULL, 1, 20) g,
			       jsonb_array_elements(g.api_keys) key
			WHERE key->>'id' = $1`, id).Scan(&n); err != nil {
			return err
		}

		if n != 1 {
			t.Errorf("the key list shows the own key %d times, want 1", n)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("creating and listing a personal key: %v", err)
	}
}

// invokableModels lists the models a personal key of userID may pick in ws.
func invokableModels(t *testing.T, db *sql.DB, userID, ws string) []string {
	t.Helper()

	var models []string

	err := asUser(t, db, userID, nil, func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT DISTINCT model FROM api.list_invokable_models($1) ORDER BY 1`, ws)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var m string
			if err := rows.Scan(&m); err != nil {
				return err
			}

			models = append(models, m)
		}

		return rows.Err()
	})
	if err != nil {
		t.Fatalf("list_invokable_models as %s failed: %v", userID, err)
	}

	return models
}

// A personal key lists what the gateway lets it call: the caller's group
// policy applies as in api.get_api_key_model_rules.
func TestModelInvoke_PersonalListFollowsGroupPolicy(t *testing.T) {
	db := GetTestDB(t)
	f := newOrgFixture(t)
	ws := newOrgName("mi-gp-ws")

	for _, m := range []string{"mi-a", "mi-b", "mi-c"} {
		insertServedModelsEndpoint(t, db, newOrgName("mi-gp-ep"), ws, servedEndpointStatus(m), false)
	}

	invoke := customRole(t, db, "model:invoke")
	newInvoker := func(prefix string) string {
		user := createGoTrueTestUser(t, prefix, "password123")
		bindGlobal(t, db, user.ID, invoke)

		return user.ID
	}

	t.Run("no group policy lists every model", func(t *testing.T) {
		if got := invokableModels(t, db, newInvoker("mi-gp-free"), ws); !reflect.DeepEqual(got, []string{"mi-a", "mi-b", "mi-c"}) {
			t.Errorf("listed %v, want mi-a, mi-b, mi-c", got)
		}
	})

	t.Run("a Team allow list narrows the list", func(t *testing.T) {
		user := newInvoker("mi-gp-team")
		team := f.createTeam()
		addToTeam(t, f, team, user)
		setGroupPolicy(t, "Team", team.Metadata.Name, gpModels("mi-a"), nil)

		if got := invokableModels(t, db, user, ws); !reflect.DeepEqual(got, []string{"mi-a"}) {
			t.Errorf("listed %v, want mi-a", got)
		}
	})

	t.Run("OrgUnit, Teams and deny combine as for the gateway", func(t *testing.T) {
		user := newInvoker("mi-gp-mix")
		unit := f.createOrgUnit("")
		t1, t2 := f.createTeam(), f.createTeam()
		putInOrgUnit(t, f, unit, user)
		addToTeam(t, f, t1, user)
		addToTeam(t, f, t2, user)
		setGroupPolicy(t, "OrgUnit", unit.Metadata.Name, gpModels("mi-a", "mi-b"), nil)
		setGroupPolicy(t, "Team", t1.Metadata.Name, gpModels("mi-a"), nil)
		setGroupPolicy(t, "Team", t2.Metadata.Name, gpModels("mi-b", "mi-c"), gpModels("mi-a"))

		got := invokableModels(t, db, user, ws)
		if !reflect.DeepEqual(got, []string{"mi-b"}) {
			t.Errorf("listed %v, want mi-b", got)
		}

		if allowed := keyAllowedModels(t, user, ws, nil); allowed != wantModels(got...) {
			t.Errorf("gateway allows %s, the list is %v", allowed, got)
		}
	})
}
