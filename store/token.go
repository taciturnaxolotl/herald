package store

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// NewToken returns a random URL-safe token for links sent by email.
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
