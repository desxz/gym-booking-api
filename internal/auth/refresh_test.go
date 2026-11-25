package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/muratgun/gym-booking-api/internal/domain"
)

var authNow = time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)

func TestRefreshTokenManagerGenerateAndHash(t *testing.T) {
	manager := NewRefreshTokenManager(7 * 24 * time.Hour)
	manager.now = func() time.Time { return authNow }
	manager.random = bytes.NewReader(bytes.Repeat([]byte{0x42}, RefreshTokenBytes))

	raw, hash, expiresAt, err := manager.Generate()
	require.NoError(t, err)
	assert.Equal(t, authNow.Add(7*24*time.Hour), expiresAt)
	assert.Equal(t, hash, manager.Hash(raw))
	assert.Equal(t, HashRefreshToken(raw), hash)

	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	require.NoError(t, err)
	assert.Len(t, decoded, RefreshTokenBytes)
	assert.Len(t, hash, 64)
	assert.NotEqual(t, raw, hash)
}

func TestRefreshTokenManagerRandomFailure(t *testing.T) {
	manager := NewRefreshTokenManager(time.Hour)
	manager.random = bytes.NewReader([]byte{1})

	_, _, _, err := manager.Generate()
	require.Error(t, err)
}

func TestHashRefreshTokenIsDeterministic(t *testing.T) {
	first := HashRefreshToken("token")
	second := HashRefreshToken("token")
	assert.Equal(t, first, second)
	assert.Len(t, first, 64)
	assert.NotEqual(t, first, HashRefreshToken("other"))
}

func TestPrincipalContext(t *testing.T) {
	principal := domain.Principal{UserID: "user", ClubID: "club", Role: domain.RoleMember}

	_, ok := PrincipalFromContext(context.Background())
	assert.False(t, ok)

	got, ok := PrincipalFromContext(WithPrincipal(context.Background(), principal))
	require.True(t, ok)
	assert.Equal(t, principal, got)
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("random source failed")
}
