package dbtest

import (
	"database/sql"
	"strings"
	"testing"
)

func TestAPIKeyProjects(t *testing.T) {
	db := GetTestDB(t)
	user := CreateTestUser(t, "project-test", "project-test@example.com", "password")

	WithUserContext(t, db, user.ID, func(tx *sql.Tx) {
		var defaultID string
		if err := tx.QueryRow(`SELECT id FROM api.list_api_key_projects('default') WHERE name='Default'`).Scan(&defaultID); err != nil {
			t.Fatalf("default project: %v", err)
		}
		if _, err := tx.Exec(`SELECT api.update_api_key_project($1, NULL, NULL, false)`, defaultID); err == nil || !strings.Contains(err.Error(), "cannot be renamed or disabled") {
			t.Fatalf("expected protected default update, got %v", err)
		}
		if _, err := tx.Exec(`SELECT api.update_api_key_project($1, 'Renamed', NULL, NULL)`, defaultID); err == nil || !strings.Contains(err.Error(), "cannot be renamed or disabled") {
			t.Fatalf("expected protected default rename, got %v", err)
		}
		if _, err := tx.Exec(`SELECT api.update_api_key_project($1, NULL, 'Updated description', NULL)`, defaultID); err != nil {
			t.Fatalf("update default description: %v", err)
		}
		if _, err := tx.Exec(`SELECT api.delete_api_key_project($1)`, defaultID); err == nil || !strings.Contains(err.Error(), "cannot be deleted") {
			t.Fatalf("expected protected default delete, got %v", err)
		}

		var source, target, disabled string
		for _, item := range []struct{ name string; enabled bool; dest *string }{
			{"Source", true, &source}, {"Target", true, &target}, {"Disabled", false, &disabled},
		} {
			err := tx.QueryRow(`SELECT id FROM api.create_api_key_project('ws-a', $1, '')`, item.name).Scan(item.dest)
			if err != nil { t.Fatalf("create project %s: %v", item.name, err) }
			if !item.enabled { if _, err = tx.Exec(`UPDATE api.api_key_projects SET enabled=false WHERE id=$1`, *item.dest); err != nil { t.Fatal(err) } }
		}

		var key1, key2 string
		if err := tx.QueryRow(`SELECT id FROM api.create_api_key('ws-a','same',0,NULL,NULL,NULL,$1,'first')`, source).Scan(&key1); err != nil { t.Fatal(err) }
		if err := tx.QueryRow(`SELECT id FROM api.create_api_key('ws-a','same',0,NULL,NULL,NULL,$1,'second')`, target).Scan(&key2); err != nil { t.Fatal(err) }

		if _, err := tx.Exec(`SELECT api.move_api_keys_to_project(ARRAY[$1::uuid],$2)`, key1, target); err == nil || !strings.Contains(err.Error(), "conflicts") { t.Fatalf("expected conflict, got %v", err) }
		if _, err := tx.Exec(`SELECT api.move_api_keys_to_project(ARRAY[$1::uuid],$2)`, key1, disabled); err == nil || !strings.Contains(err.Error(), "disabled") { t.Fatalf("expected disabled error, got %v", err) }
		if _, err := tx.Exec(`SELECT api.delete_api_key_project($1)`, source); err == nil || !strings.Contains(err.Error(), "1 API keys") { t.Fatalf("expected protected delete, got %v", err) }

		if _, err := tx.Exec(`DELETE FROM api.api_keys WHERE id=$1`, key2); err != nil { t.Fatal(err) }
		var moved int
		if err := tx.QueryRow(`SELECT api.move_api_keys_to_project(ARRAY[$1::uuid],$2)`, key1, target).Scan(&moved); err != nil { t.Fatal(err) }
		if moved != 1 { t.Fatalf("moved=%d want 1", moved) }
		var history int
		if err := tx.QueryRow(`SELECT count(*) FROM api.api_key_project_history WHERE api_key_id=$1`, key1).Scan(&history); err != nil || history != 1 { t.Fatalf("history=%d err=%v", history, err) }

		var count, total int
		if err := tx.QueryRow(`SELECT api_key_count, total_projects FROM api.get_api_key_project_groups('ws-a',NULL,NULL,NULL,1,1) LIMIT 1`).Scan(&count, &total); err != nil { t.Fatal(err) }
		if total != 3 { t.Fatalf("total projects=%d want 3", total) }

		if _, err := tx.Exec(`SELECT api.create_api_key_project('ws-b', 'Other workspace', '')`); err != nil { t.Fatal(err) }
		if err := tx.QueryRow(`SELECT max(total_projects) FROM api.get_api_key_project_groups(NULL,NULL,NULL,NULL,1,20)`).Scan(&total); err != nil { t.Fatal(err) }
		if total != 4 { t.Fatalf("all-workspace total=%d want 4", total) }

		var keysJSON string
		if err := tx.QueryRow(`SELECT api_keys::text FROM api.get_api_key_project_groups('ws-a','Target',NULL,NULL,1,20) WHERE (project).id=$1`, target).Scan(&keysJSON); err != nil { t.Fatal(err) }
		if !strings.Contains(keysJSON, `"name": "same"`) { t.Fatalf("project match must return all keys: %s", keysJSON) }
		if err := tx.QueryRow(`SELECT api_keys::text FROM api.get_api_key_project_groups('ws-a','first',NULL,NULL,1,20) WHERE (project).id=$1`, target).Scan(&keysJSON); err != nil { t.Fatal(err) }
		if !strings.Contains(keysJSON, `"description": "first"`) { t.Fatalf("key search did not return match: %s", keysJSON) }
	})
}
