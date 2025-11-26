package dbtest

import (
	"context"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func Test_AdminPassword_Set(t *testing.T) {
	db := GetTestDB(t)
	ctx := context.Background()

	var encryptedPassword string
	err := db.QueryRowContext(ctx, `
		SELECT encrypted_password FROM auth.users WHERE email = 'admin@neutree.local'
	`).Scan(&encryptedPassword)
	if err != nil {
		t.Fatalf("failed to query admin password setting: %v", err)
	}

	expectedPassword := "neutree-test"

	err = bcrypt.CompareHashAndPassword([]byte(encryptedPassword), []byte(expectedPassword))
	if err != nil {
		t.Fatalf("expected admin password set, but comparison failed: %v", err)
	}

	t.Logf("admin password set successfully")
}
