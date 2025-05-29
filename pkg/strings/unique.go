package strings

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/OneOfOne/xxhash"
)

// UniqueString implements Azure's UniqueString algorithm for deterministic resource naming
func UniqueString(inputs ...string) string {
	h := xxhash.New64()
	for _, input := range inputs {
		_, _ = h.Write([]byte(strings.ToLower(input)))
	}

	v := h.Sum64()
	id := strconv.FormatUint(v, 36)

	return id
}

// DeterministicGUID returns a deterministic GUID (UUID v4 format) from a string
func DeterministicGUID(input string) string {
	hash := sha256.Sum256([]byte(input))
	b := hash[:16] // Use the first 16 bytes for UUID
	// Set version (4) and variant bits for UUID v4
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return hex.EncodeToString(b[0:4]) + "-" +
		hex.EncodeToString(b[4:6]) + "-" +
		hex.EncodeToString(b[6:8]) + "-" +
		hex.EncodeToString(b[8:10]) + "-" +
		hex.EncodeToString(b[10:16])
}

// GeneratePassword creates a random password of the given length
func GeneratePassword(length int) (string, error) {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, length)
	for i := range b {
		idx, err := randomInt(len(charset))
		if err != nil {
			return "", err
		}
		b[i] = charset[idx]
	}
	return string(b), nil
}

// randomInt returns a random int in [0, max) using crypto/rand for security
func randomInt(max int) (int, error) {
	if max <= 0 {
		return 0, fmt.Errorf("max must be positive")
	}
	var b [1]byte
	_, err := rand.Read(b[:])
	if err != nil {
		return 0, err
	}
	return int(b[0]) % max, nil
}
