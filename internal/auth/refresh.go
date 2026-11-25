package auth

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"time"

	"crypto/sha256"
)

// RefreshTokenBytes is the raw entropy size of a refresh token.
const RefreshTokenBytes = 32

// RefreshTokenManager generates opaque refresh tokens and their SHA-256 hashes.
// Only the hash is persisted.
type RefreshTokenManager struct {
	ttl    time.Duration
	now    func() time.Time
	random io.Reader
}

func NewRefreshTokenManager(ttl time.Duration) *RefreshTokenManager {
	return &RefreshTokenManager{
		ttl:    ttl,
		now:    func() time.Time { return time.Now().UTC() },
		random: rand.Reader,
	}
}

// Generate returns a fresh token, its storage hash, and its expiry.
func (m *RefreshTokenManager) Generate() (raw string, hash string, expiresAt time.Time, err error) {
	entropy, err := readRandom(m.random, RefreshTokenBytes)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("generate refresh token: %w", err)
	}
	raw = base64.RawURLEncoding.EncodeToString(entropy)
	return raw, HashRefreshToken(raw), m.now().Add(m.ttl), nil
}

// Hash can be used on an incoming token before lookup.
func (m *RefreshTokenManager) Hash(raw string) string {
	return HashRefreshToken(raw)
}

// HashRefreshToken returns the lowercase hex SHA-256 digest of a raw token.
func HashRefreshToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func readRandom(reader io.Reader, size int) ([]byte, error) {
	buf := make([]byte, size)
	if _, err := io.ReadFull(reader, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func randomHex(reader io.Reader, size int) (string, error) {
	buf, err := readRandom(reader, size)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
