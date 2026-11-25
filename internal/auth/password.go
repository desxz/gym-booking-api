package auth

import (
	"fmt"

	"golang.org/x/crypto/bcrypt"

	"github.com/muratgun/gym-booking-api/internal/domain"
)

// DummyPasswordHash is a valid cost-12 hash used to give unknown-user logins the
// same bcrypt cost as wrong-password logins. It is not a secret.
const DummyPasswordHash = "$2a$12$LPn6rV87rRQQAbu/ubXgg..01hovEJBEGbCsKILtzBOUtJmKFvGXa"

// Hasher owns the bcrypt cost so the service can stay independent of bcrypt.
type Hasher struct {
	cost int
}

func NewHasher(cost int) *Hasher {
	return &Hasher{cost: cost}
}

// HashPassword validates length before hashing: bcrypt truncates at 72 bytes,
// so accepting longer input would make distinct passwords equivalent.
func (h *Hasher) HashPassword(plain string) (string, error) {
	if err := validatePassword(plain); err != nil {
		return "", err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(plain), h.cost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(hash), nil
}

// VerifyPassword returns an error for a wrong password, a malformed hash, or a
// length outside bcrypt's range; callers must treat every error as invalid
// credentials.
func (h *Hasher) VerifyPassword(hash, plain string) error {
	if err := validatePassword(plain); err != nil {
		return err
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain))
}

func validatePassword(plain string) error {
	length := len([]byte(plain))
	if length >= domain.MinPasswordBytes && length <= domain.MaxPasswordBytes {
		return nil
	}
	return &domain.ValidationError{Fields: map[string]string{
		"password": fmt.Sprintf("must be between %d and %d bytes", domain.MinPasswordBytes, domain.MaxPasswordBytes),
	}}
}
