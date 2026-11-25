package auth

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/muratgun/gym-booking-api/internal/domain"
)

func TestHasherHashAndVerify(t *testing.T) {
	hasher := NewHasher(4) // low cost keeps the unit test fast; config enforces 10–14

	hash, err := hasher.HashPassword("Password123!")
	require.NoError(t, err)
	require.NoError(t, hasher.VerifyPassword(hash, "Password123!"))
	require.Error(t, hasher.VerifyPassword(hash, "wrong-password"))

	_, err = NewHasher(40).HashPassword("Password123!")
	require.Error(t, err)
}

func TestPasswordLengthBoundaries(t *testing.T) {
	hasher := NewHasher(4)
	valid := []string{
		strings.Repeat("a", domain.MinPasswordBytes),
		strings.Repeat("a", domain.MaxPasswordBytes),
	}
	for _, password := range valid {
		_, err := hasher.HashPassword(password)
		require.NoError(t, err, "%d-byte password", len(password))
	}

	invalid := []string{
		strings.Repeat("a", domain.MinPasswordBytes-1),
		strings.Repeat("a", domain.MaxPasswordBytes+1),
	}
	for _, password := range invalid {
		_, err := hasher.HashPassword(password)
		var validation *domain.ValidationError
		require.ErrorAs(t, err, &validation)

		err = hasher.VerifyPassword(DummyPasswordHash, password)
		require.ErrorAs(t, err, &validation)
	}
}

func TestDummyPasswordHash(t *testing.T) {
	cost, err := bcrypt.Cost([]byte(DummyPasswordHash))
	require.NoError(t, err)
	assert.Equal(t, 12, cost)
}
