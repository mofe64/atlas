package authservice

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

const (
	sessionTokenPrefix = "atlas_session_"
	sessionTokenBytes  = 32
)

// newSessionToken returns the raw bearer token for the client and its digest for
// database storage. The raw token must never be persisted by the backend.
func newSessionToken() (string, []byte, error) {
	randomBytes := make([]byte, sessionTokenBytes)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", nil, fmt.Errorf("generate session token: %w", err)
	}
	raw := sessionTokenPrefix + base64.RawURLEncoding.EncodeToString(randomBytes)
	return raw, hashSessionToken(raw), nil
}

// validateAndHashSessionToken validates the public token format and returns the
// digest used for database lookup.
func validateAndHashSessionToken(raw string) ([]byte, error) {
	if !strings.HasPrefix(raw, sessionTokenPrefix) {
		return nil, errors.New("session token has an invalid prefix")
	}
	encoded := strings.TrimPrefix(raw, sessionTokenPrefix)
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(decoded) != sessionTokenBytes {
		return nil, errors.New("session token has an invalid value")
	}
	return hashSessionToken(raw), nil
}

func hashSessionToken(raw string) []byte {
	digest := sha256.Sum256([]byte(raw))
	return digest[:]
}
