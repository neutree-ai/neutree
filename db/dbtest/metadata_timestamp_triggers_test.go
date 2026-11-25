package dbtest

import (
	"testing"
)

// Every table in schema api with a metadata column must carry the timestamp
// trigger pair, otherwise update_timestamp silently stops advancing (NEU-857).
func TestMetadataTablesHaveTimestampTriggers(t *testing.T) {
	db := GetTestDB(t)

	const query = `
SELECT c.relname,
       EXISTS (
           SELECT 1 FROM pg_trigger tg JOIN pg_proc p ON p.oid = tg.tgfoid
           WHERE tg.tgrelid = c.oid AND NOT tg.tgisinternal
             AND p.proname = $1
             AND (tg.tgtype & 1) = 1    -- row level
             AND (tg.tgtype & 2) = 2    -- BEFORE
             AND (tg.tgtype & $2) = $2
       )
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN pg_attribute a ON a.attrelid = c.oid AND a.attname = 'metadata' AND NOT a.attisdropped
JOIN pg_type ty ON ty.oid = a.atttypid
JOIN pg_namespace tn ON tn.oid = ty.typnamespace
WHERE n.nspname = 'api' AND c.relkind IN ('r', 'p')
  AND tn.nspname = 'api' AND ty.typname = 'metadata'
ORDER BY c.relname`

	checks := []struct {
		name    string
		fn      string
		tgEvent int // pg_trigger.tgtype bit: 4 = INSERT, 16 = UPDATE
	}{
		{"BEFORE UPDATE", "update_metadata_update_timestamp_column", 16},
		{"BEFORE INSERT", "set_default_metadata_timestamp_column", 4},
	}

	for _, check := range checks {
		rows, err := db.Query(query, check.fn, check.tgEvent)
		if err != nil {
			t.Fatalf("failed to query triggers: %v", err)
		}

		var missing []string

		tables := 0

		for rows.Next() {
			var table string

			var ok bool
			if err := rows.Scan(&table, &ok); err != nil {
				rows.Close()
				t.Fatalf("failed to scan: %v", err)
			}

			tables++

			if !ok {
				missing = append(missing, table)
			}
		}

		rows.Close()

		if err := rows.Err(); err != nil {
			t.Fatalf("failed to read rows: %v", err)
		}

		if tables == 0 {
			t.Fatal("found no api tables with a metadata column")
		}

		if len(missing) > 0 {
			t.Errorf("tables missing a %s row trigger calling %s: %v", check.name, check.fn, missing)
		}
	}
}
