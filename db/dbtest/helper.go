package dbtest

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"

	_ "github.com/lib/pq"
	"github.com/pkg/errors"
	"github.com/supabase-community/gotrue-go"
	"github.com/supabase-community/gotrue-go/types"

	"github.com/neutree-ai/neutree/pkg/storage"
)

// grantGlobalPermissions creates a role carrying the given permissions and
// assigns it globally to an existing user. In the community edition
// api.has_permission ignores the workspace argument and degrades to a global
// check, so a global assignment is what exercises the permission.
func grantGlobalPermissions(t *testing.T, db *sql.DB, userID string, permissions []string) {
	t.Helper()
	ctx := context.Background()
	roleName := "perms-" + userID
	permsArray := "ARRAY['" + strings.Join(permissions, "','") + "']::api.permission_action[]"

	if _, err := db.ExecContext(ctx, `
		INSERT INTO api.roles (api_version, kind, metadata, spec)
		VALUES ('v1', 'Role',
			ROW($1, NULL, NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
			ROW(NULL, `+permsArray+`)::api.role_spec)`, roleName); err != nil {
		t.Fatalf("failed to create role: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO api.role_assignments (api_version, kind, metadata, spec)
		VALUES ('v1', 'RoleAssignment',
			ROW($1, NULL, NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
			ROW($2::uuid, NULL, TRUE, $3)::api.role_assignment_spec)`,
		"assign-"+userID, userID, roleName); err != nil {
		t.Fatalf("failed to create role assignment: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, `DELETE FROM api.role_assignments WHERE (metadata).name = $1`, "assign-"+userID)
		_, _ = db.ExecContext(ctx, `DELETE FROM api.roles WHERE (metadata).name = $1`, roleName)
	})
}

// GetTestDB returns a connection to the test database
// The database is pre-migrated by docker-compose
func GetTestDB(t *testing.T) *sql.DB {
	t.Helper()

	// Get PostgreSQL connection details from environment or use defaults
	host := getEnvOrDefault("POSTGRES_HOST", "localhost")
	port := getEnvOrDefault("POSTGRES_PORT", "5432")
	user := getEnvOrDefault("POSTGRES_USER", "postgres")
	password := getEnvOrDefault("POSTGRES_PASSWORD", "pgpassword")
	dbname := getEnvOrDefault("POSTGRES_DB", "neutree_test")

	// Connection string
	connStr := fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		host, port, user, password, dbname,
	)

	db, err := sql.Open("postgres", connStr)
	if err != nil {
		t.Fatalf("failed to connect to database: %v", err)
	}

	// Verify connection
	if err := db.Ping(); err != nil {
		t.Fatalf("failed to ping database: %v", err)
	}

	// Cleanup function to close DB
	t.Cleanup(func() {
		db.Close()
	})

	return db
}

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}

	return defaultValue
}

type TestUser struct {
	ID    string
	Email string
}

func CreateTestUser(t *testing.T, username, email, password string) *TestUser {
	t.Helper()

	token, err := storage.CreateServiceToken("test-jwt-secret-32-characters-min")
	if err != nil {
		t.Fatalf("failed to create service token: %v", err)
	}

	client := gotrue.New("", "").WithCustomGoTrueURL("http://localhost:9999").WithToken(*token)

	resp, err := client.AdminCreateUser(types.AdminCreateUserRequest{
		Email:        email,
		Password:     &password,
		EmailConfirm: true,
		UserMetadata: map[string]any{
			"username": username,
		},
	})
	if err != nil {
		t.Fatalf("failed to create test user: %v", err)
	}

	return &TestUser{
		ID:    resp.User.ID.String(),
		Email: resp.User.Email,
	}
}

func WithUserContext(t *testing.T, db *sql.DB, userID string, fn func(*sql.Tx)) {
	t.Helper()

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	defer func() {
		_ = tx.Rollback()
	}()

	_, err = tx.Exec(fmt.Sprintf("SET LOCAL request.jwt.claim.sub = '%s'", userID))
	if err != nil {
		t.Fatalf("failed to set user context: %v", err)
	}

	fn(tx)

	if err := tx.Commit(); err != nil {
		t.Fatalf("failed to commit transaction: %v", err)
	}
}

type SetContextFunc func(tx *sql.Tx) error

func setUserContext(userID string) SetContextFunc {
	return func(tx *sql.Tx) error {
		_, err := tx.Exec(fmt.Sprintf("SET LOCAL request.jwt.claim.sub = '%s'", userID))
		return errors.Wrapf(err, "failed to set user context for user %s", userID)
	}
}

// testJwtSecret is the JWT secret used by the database test context. It is the
// only value ever needed, so setJwtSecretContext takes no parameter (avoids the
// unparam lint about an always-constant argument).
const testJwtSecret = "test"

func setJwtSecretContext() SetContextFunc {
	return func(tx *sql.Tx) error {
		_, err := tx.Exec(fmt.Sprintf("SET LOCAL app.settings.jwt_secret = '%s'", testJwtSecret))
		return errors.Wrap(err, "failed to set jwt secret context")
	}
}

func execWithContext(t *testing.T, db *sql.DB, ctxFuncs []SetContextFunc, fn func(*sql.Tx) error) error {
	t.Helper()

	ctx := context.Background()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	defer func() {
		_ = tx.Rollback()
	}()

	for _, set := range ctxFuncs {
		if err := set(tx); err != nil {
			t.Fatalf("failed to set context: %v", err)
		}
	}

	if err := fn(tx); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("failed to commit transaction: %v", err)
	}

	return nil
}
