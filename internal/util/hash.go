package util

import (
	"crypto/sha256"
	"fmt"
)

func HashString(s string) string {
	hash := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", hash)[:10]
}
