package dbtest

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

const validLogSinkVictoriaLogs = `{"url": "https://vl.example.com:9428", "auth": {"type": "bearer", "token": "secret"}}`

func insertLogSink(ctx context.Context, tx *sql.Tx, name, sinkType, victorialogs, buffer string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO api.log_sinks (api_version, kind, metadata, spec)
		VALUES (
			'v1',
			'LogSink',
			ROW($1, NULL, NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
			ROW($2::text, NULL, $3::jsonb, $4::jsonb)::api.log_sink_spec
		)
	`, name, sinkType, nullIfEmpty(victorialogs), nullIfEmpty(buffer))

	return err
}

func nullIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}

	return s
}

func countLogSinks(ctx context.Context, tx *sql.Tx, name string) (int, error) {
	var count int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM api.log_sinks WHERE (metadata).name = $1`, name).Scan(&count)

	return count, err
}

func TestLogSinkRBAC_UserWithoutPermissionIsBlocked(t *testing.T) {
	adminDB := GetTestDB(t)
	ctx := context.Background()

	if _, err := adminDB.ExecContext(ctx, `
		INSERT INTO api.log_sinks (api_version, kind, metadata, spec)
		VALUES ('v1', 'LogSink',
			ROW('log-sink-hidden', NULL, NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
			ROW('victorialogs', NULL, $1::jsonb, NULL)::api.log_sink_spec)
	`, validLogSinkVictoriaLogs); err != nil {
		t.Fatalf("failed to seed log sink: %v", err)
	}

	tx, err := adminDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	// A workspace-wide reader still cannot see or create global log sinks.
	userID := createUserWithPermissions(t, tx, "log-sink-outsider", "log-sink-outsider@test.local",
		[]string{"endpoint:read", "image_registry:read"})

	if err = tx.Commit(); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	err = executeAsUser(t, adminDB, userID, func(tx *sql.Tx) error {
		count, err := countLogSinks(ctx, tx, "log-sink-hidden")
		if err != nil {
			return err
		}
		if count != 0 {
			t.Fatalf("expected log sinks to be hidden, got %d", count)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err = executeAsUser(t, adminDB, userID, func(tx *sql.Tx) error {
		return insertLogSink(ctx, tx, "log-sink-outsider-create", "victorialogs", validLogSinkVictoriaLogs, "")
	})
	if err == nil || !strings.Contains(err.Error(), "row-level security") {
		t.Fatalf("expected RLS to block insert, got: %v", err)
	}
}

func TestLogSinkRBAC_ReadOnlyUserCannotWrite(t *testing.T) {
	adminDB := GetTestDB(t)
	ctx := context.Background()

	tx, err := adminDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	userID := createUserWithPermissions(t, tx, "log-sink-reader", "log-sink-reader@test.local", []string{"log_sink:read"})

	if err = tx.Commit(); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	err = executeAsUser(t, adminDB, userID, func(tx *sql.Tx) error {
		return insertLogSink(ctx, tx, "log-sink-reader-create", "victorialogs", validLogSinkVictoriaLogs, "")
	})
	if err == nil || !strings.Contains(err.Error(), "row-level security") {
		t.Fatalf("expected RLS to block insert without log_sink:create, got: %v", err)
	}
}

func TestLogSinkRBAC_AdminFullLifecycle(t *testing.T) {
	adminDB := GetTestDB(t)
	ctx := context.Background()

	tx, err := adminDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	userID := createUserWithPresetRole(t, tx, "log-sink-admin", "log-sink-admin@test.local", "admin")

	if err = tx.Commit(); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	err = executeAsUser(t, adminDB, userID, func(tx *sql.Tx) error {
		for _, permission := range []string{
			"log_sink:read", "log_sink:create", "log_sink:update", "log_sink:delete", "log_sink:read-credentials",
		} {
			var allowed bool
			if err := tx.QueryRowContext(ctx,
				`SELECT api.has_permission($1::uuid, $2::api.permission_action, NULL)`, userID, permission,
			).Scan(&allowed); err != nil {
				return err
			}
			if !allowed {
				t.Fatalf("expected admin to hold %s", permission)
			}
		}

		if err := insertLogSink(ctx, tx, "log-sink-admin-a", "victorialogs", validLogSinkVictoriaLogs, ""); err != nil {
			t.Fatalf("admin insert failed: %v", err)
		}

		// Names are unique across the whole table, not per workspace.
		if err := insertLogSink(ctx, tx, "log-sink-admin-a", "victorialogs", validLogSinkVictoriaLogs, ""); err == nil {
			t.Fatal("expected duplicate name to be rejected")
		}

		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err = executeAsUser(t, adminDB, userID, func(tx *sql.Tx) error {
		if err := insertLogSink(ctx, tx, "log-sink-admin-b", "victorialogs", validLogSinkVictoriaLogs, ""); err != nil {
			t.Fatalf("admin insert failed: %v", err)
		}

		if count, err := countLogSinks(ctx, tx, "log-sink-admin-b"); err != nil || count != 1 {
			t.Fatalf("expected admin to read the log sink, count=%d err=%v", count, err)
		}

		res, err := tx.ExecContext(ctx, `
			UPDATE api.log_sinks
			SET spec.enabled = FALSE
			WHERE (metadata).name = 'log-sink-admin-b'
		`)
		if err != nil {
			t.Fatalf("admin update failed: %v", err)
		}
		if rows, _ := res.RowsAffected(); rows != 1 {
			t.Fatalf("expected 1 row updated, got %d", rows)
		}

		if _, err := tx.ExecContext(ctx, `
			UPDATE api.log_sinks
			SET metadata.deletion_timestamp = now()
			WHERE (metadata).name = 'log-sink-admin-b'
		`); err != nil {
			t.Fatalf("admin soft delete failed: %v", err)
		}

		res, err = tx.ExecContext(ctx, `DELETE FROM api.log_sinks WHERE (metadata).name = 'log-sink-admin-b'`)
		if err != nil {
			t.Fatalf("admin hard delete failed: %v", err)
		}
		if rows, _ := res.RowsAffected(); rows != 1 {
			t.Fatalf("expected 1 row deleted, got %d", rows)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLogSinkSoftDelete(t *testing.T) {
	adminDB := GetTestDB(t)
	ctx := context.Background()

	if _, err := adminDB.ExecContext(ctx, `
		INSERT INTO api.log_sinks (api_version, kind, metadata, spec)
		VALUES ('v1', 'LogSink',
			ROW('log-sink-soft-delete', NULL, NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
			ROW('victorialogs', NULL, $1::jsonb, NULL)::api.log_sink_spec)
	`, validLogSinkVictoriaLogs); err != nil {
		t.Fatalf("failed to seed log sink: %v", err)
	}

	tx, err := adminDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	updaterID := createUserWithPermissions(t, tx, "log-sink-updater", "log-sink-updater@test.local",
		[]string{"log_sink:read", "log_sink:update"})
	deleterID := createUserWithPermissions(t, tx, "log-sink-deleter", "log-sink-deleter@test.local",
		[]string{"log_sink:read", "log_sink:delete"})

	if err = tx.Commit(); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	t.Run("update permission alone cannot soft delete", func(t *testing.T) {
		err := executeAsUser(t, adminDB, updaterID, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `
				UPDATE api.log_sinks
				SET metadata.deletion_timestamp = now()
				WHERE (metadata).name = 'log-sink-soft-delete'
			`)
			return err
		})
		if err == nil || !strings.Contains(err.Error(), "row-level security") {
			t.Fatalf("expected RLS to block soft delete, got: %v", err)
		}
	})

	t.Run("soft delete cannot modify spec", func(t *testing.T) {
		err := executeAsUser(t, adminDB, deleterID, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `
				UPDATE api.log_sinks
				SET metadata.deletion_timestamp = now(), spec.enabled = FALSE
				WHERE (metadata).name = 'log-sink-soft-delete'
			`)
			return err
		})
		if err == nil || !strings.Contains(err.Error(), "Cannot modify spec during soft delete operation") {
			t.Fatalf("expected soft delete integrity error, got: %v", err)
		}
	})

	t.Run("delete permission can soft delete", func(t *testing.T) {
		err := executeAsUser(t, adminDB, deleterID, func(tx *sql.Tx) error {
			res, err := tx.ExecContext(ctx, `
				UPDATE api.log_sinks
				SET metadata.deletion_timestamp = now()
				WHERE (metadata).name = 'log-sink-soft-delete'
			`)
			if err != nil {
				return err
			}
			if rows, _ := res.RowsAffected(); rows != 1 {
				t.Fatalf("expected 1 row soft deleted, got %d", rows)
			}

			return nil
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestLogSinkValidation(t *testing.T) {
	db := GetTestDB(t)
	ctx := context.Background()

	tests := []struct {
		name         string
		sinkType     string
		victorialogs string
		buffer       string
		wantCode     string
	}{
		{name: "unsupported type", sinkType: "s3", victorialogs: validLogSinkVictoriaLogs, wantCode: "10243"},
		{name: "missing type", sinkType: "", victorialogs: validLogSinkVictoriaLogs, wantCode: "10243"},
		{name: "missing victorialogs", sinkType: "victorialogs", wantCode: "10244"},
		{name: "empty url", sinkType: "victorialogs", victorialogs: `{"url": " "}`, wantCode: "10244"},
		{name: "url without scheme", sinkType: "victorialogs", victorialogs: `{"url": "vl.example.com:9428"}`, wantCode: "10245"},
		{name: "url with other scheme", sinkType: "victorialogs", victorialogs: `{"url": "ftp://vl.example.com"}`, wantCode: "10245"},
		{name: "unknown auth type", sinkType: "victorialogs", victorialogs: `{"url": "http://vl", "auth": {"type": "digest"}}`, wantCode: "10246"},
		{name: "auth without type", sinkType: "victorialogs", victorialogs: `{"url": "http://vl", "auth": {"token": "t"}}`, wantCode: "10246"},
		{name: "basic without username", sinkType: "victorialogs", victorialogs: `{"url": "http://vl", "auth": {"type": "basic", "password": "p"}}`, wantCode: "10247"},
		{name: "bearer without token", sinkType: "victorialogs", victorialogs: `{"url": "http://vl", "auth": {"type": "bearer"}}`, wantCode: "10248"},
		{name: "buffer below minimum", sinkType: "victorialogs", victorialogs: `{"url": "http://vl"}`, buffer: `{"max_size": 268435487}`, wantCode: "10249"},
		{name: "buffer not a number", sinkType: "victorialogs", victorialogs: `{"url": "http://vl"}`, buffer: `{"max_size": "1Gi"}`, wantCode: "10249"},
		{name: "minimal valid", sinkType: "victorialogs", victorialogs: `{"url": "http://vl"}`},
		{name: "auth none", sinkType: "victorialogs", victorialogs: `{"url": "HTTPS://vl", "auth": {"type": "none"}}`},
		{name: "basic with username", sinkType: "victorialogs", victorialogs: `{"url": "http://vl", "auth": {"type": "basic", "username": "u"}}`},
		{name: "buffer at minimum", sinkType: "victorialogs", victorialogs: `{"url": "http://vl"}`, buffer: `{"max_size": 268435488}`},
		{name: "empty buffer", sinkType: "victorialogs", victorialogs: `{"url": "http://vl"}`, buffer: `{}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatalf("failed to begin transaction: %v", err)
			}
			defer func() {
				_ = tx.Rollback()
			}()

			err = insertLogSink(ctx, tx, "log-sink-validation", tt.sinkType, tt.victorialogs, tt.buffer)

			if tt.wantCode == "" {
				if err != nil {
					t.Fatalf("expected insert to succeed, got: %v", err)
				}

				return
			}

			if err == nil || !strings.Contains(err.Error(), `"code": "`+tt.wantCode+`"`) {
				t.Fatalf("expected error code %s, got: %v", tt.wantCode, err)
			}
		})
	}

	t.Run("update is validated too", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("failed to begin transaction: %v", err)
		}
		defer func() {
			_ = tx.Rollback()
		}()

		if err := insertLogSink(ctx, tx, "log-sink-validation-update", "victorialogs", validLogSinkVictoriaLogs, ""); err != nil {
			t.Fatalf("insert failed: %v", err)
		}

		_, err = tx.ExecContext(ctx, `
			UPDATE api.log_sinks
			SET spec.victorialogs = '{"url": "http://vl", "auth": {"type": "bearer"}}'::jsonb
			WHERE (metadata).name = 'log-sink-validation-update'
		`)
		if err == nil || !strings.Contains(err.Error(), `"code": "10248"`) {
			t.Fatalf("expected error code 10248, got: %v", err)
		}
	})
}

func TestLogSinkNormalization(t *testing.T) {
	db := GetTestDB(t)
	ctx := context.Background()

	run := func(t *testing.T, vl string, update string) (auth sql.NullString, enabled sql.NullBool, workspace sql.NullString, err error) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("failed to begin transaction: %v", err)
		}
		defer func() {
			_ = tx.Rollback()
		}()

		if err := insertLogSink(ctx, tx, "log-sink-normalize", "victorialogs", vl, ""); err != nil {
			t.Fatalf("insert failed: %v", err)
		}

		if update != "" {
			if _, err = tx.ExecContext(ctx, `UPDATE api.log_sinks SET `+update+` WHERE (metadata).name = 'log-sink-normalize'`); err != nil {
				return auth, enabled, workspace, err
			}
		}

		err = tx.QueryRowContext(ctx, `
			SELECT (spec).victorialogs->'auth', (spec).enabled, (metadata).workspace
			FROM api.log_sinks WHERE (metadata).name = 'log-sink-normalize'
		`).Scan(&auth, &enabled, &workspace)

		return auth, enabled, workspace, err
	}

	t.Run("enabled defaults to true and workspace is cleared", func(t *testing.T) {
		_, enabled, workspace, err := run(t, `{"url": "http://vl"}`, `metadata.workspace = 'default'`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !enabled.Valid || !enabled.Bool {
			t.Fatalf("expected enabled=true, got %+v", enabled)
		}
		if workspace.Valid {
			t.Fatalf("expected no workspace, got %q", workspace.String)
		}
	})

	t.Run("explicit enabled=false is kept", func(t *testing.T) {
		_, enabled, _, err := run(t, `{"url": "http://vl"}`, `spec.enabled = false`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !enabled.Valid || enabled.Bool {
			t.Fatalf("expected enabled=false, got %+v", enabled)
		}
	})

	t.Run("unused secrets are dropped", func(t *testing.T) {
		auth, _, _, err := run(t, `{"url": "http://vl", "auth": {"type": "basic", "username": "u", "password": "p"}}`,
			`spec.victorialogs = '{"url": "http://vl", "auth": {"type": "bearer", "token": "t", "username": "u", "password": "p"}}'::jsonb`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if auth.String != `{"type": "bearer", "token": "t"}` {
			t.Fatalf("expected only the bearer token, got %s", auth.String)
		}
	})

	t.Run("backfilled secrets without type remove auth", func(t *testing.T) {
		auth, _, _, err := run(t, validLogSinkVictoriaLogs,
			`spec.victorialogs = '{"url": "http://vl", "auth": {"token": "secret"}}'::jsonb`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if auth.Valid {
			t.Fatalf("expected auth removed, got %s", auth.String)
		}
	})

	t.Run("new secret without type is still rejected", func(t *testing.T) {
		_, _, _, err := run(t, validLogSinkVictoriaLogs,
			`spec.victorialogs = '{"url": "http://vl", "auth": {"token": "other"}}'::jsonb`)
		if err == nil || !strings.Contains(err.Error(), `"code": "10246"`) {
			t.Fatalf("expected error code 10246, got: %v", err)
		}
	})
}
