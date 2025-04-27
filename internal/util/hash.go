package util

import "crypto/sha256"

func HashString(s string) []byte {
	hash := sha256.Sum256([]byte(s))
	return hash[:]
}
