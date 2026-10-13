package e2e

import (
	cryptorand "crypto/rand"
	"errors"
	"testing"
)

type failingRunIDReader struct{}

func (failingRunIDReader) Read([]byte) (int, error) {
	return 0, errors.New("randomness unavailable")
}

func TestGenerateRunID_Uses18DigitSuffix(t *testing.T) {
	runID := generateRunID()
	if len(runID) != 18 {
		t.Fatalf("RunID length = %d, want 18; value=%q", len(runID), runID)
	}

	for _, character := range runID {
		if character < '0' || character > '9' {
			t.Fatalf("RunID contains non-digit %q in %q", character, runID)
		}
	}
}

func TestGenerateRunID_PanicsWhenRandomnessUnavailable(t *testing.T) {
	originalReader := cryptorand.Reader
	cryptorand.Reader = failingRunIDReader{}
	t.Cleanup(func() {
		cryptorand.Reader = originalReader
	})

	defer func() {
		if recover() == nil {
			t.Fatal("generateRunID should fail without cryptographic randomness")
		}
	}()

	generateRunID()
}
