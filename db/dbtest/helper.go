package dbtest

import (
	"database/sql"
	"fmt"
	"os"
	"testing"

	_ "github.com/lib/pq"
	"github.com/neutree-ai/neutree/pkg/storage"
	"github.com/supabase-community/gotrue-go"
	"github.com/supabase-community/gotrue-go/types"
)

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
	defer tx.Rollback()

	_, err = tx.Exec(fmt.Sprintf("SET LOCAL request.jwt.claim.sub = '%s'", userID))
	if err != nil {
		t.Fatalf("failed to set user context: %v", err)
	}

	fn(tx)

	if err := tx.Commit(); err != nil {
		t.Fatalf("failed to commit transaction: %v", err)
	}
}
