package e2e

import (
	"bufio"
	"crypto/rand"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
)

// E2EConfig holds infrastructure-level configuration that is not part of
// the test profile. Test-specific config lives in profile.go.
type E2EConfig struct {
	ServerURL string // NEUTREE_SERVER_URL (required)
	APIKey    string // NEUTREE_API_KEY (required)
	RunID     string // auto-generated 6-digit random suffix for resource name uniqueness
}

// Cfg is the global e2e configuration, loaded once at package init.
var Cfg = loadConfig()

func loadConfig() *E2EConfig {
	// Try to load .env file from test directory
	loadDotEnv()

	return &E2EConfig{
		ServerURL: os.Getenv("NEUTREE_SERVER_URL"),
		APIKey:    os.Getenv("NEUTREE_API_KEY"),
		RunID:     generateRunID(),
	}
}

func generateRunID() string {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "000000"
	}

	return fmt.Sprintf("%06d", n.Int64())
}

// loadDotEnv loads a .env file if present. It does NOT override existing env vars.
func loadDotEnv() {
	// Look for .env in the test directory (where go test runs)
	paths := []string{".env", filepath.Join("tests", "e2e", ".env")}

	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		defer f.Close()

		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}

			key, value, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}

			key = strings.TrimSpace(key)
			value = strings.TrimSpace(value)

			// Don't override existing env vars
			if os.Getenv(key) == "" {
				os.Setenv(key, value)
			}
		}

		return // loaded one file, done
	}
}
