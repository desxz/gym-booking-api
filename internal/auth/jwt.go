package auth

import (
	"crypto/rand"
	"fmt"
	"io"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/muratgun/gym-booking-api/internal/config"
	"github.com/muratgun/gym-booking-api/internal/domain"
)

const (
	MinJWTSecretBytes = 32
	BearerPrefix      = "Bearer "
	TokenTypeBearer   = "Bearer"
	jtiBytes          = 16
)

// TokenManager issues and verifies HS256 access tokens.
type TokenManager struct {
	issuer string
	ttl    time.Duration
	secret []byte
	now    func() time.Time
	random io.Reader
	sign   func(jwt.Claims) (string, error)
}

func NewTokenManager(issuer string, ttl time.Duration, secret config.Secret) *TokenManager {
	key := []byte(secret.Expose())
	return &TokenManager{
		issuer: issuer,
		ttl:    ttl,
		secret: key,
		now:    func() time.Time { return time.Now().UTC() },
		random: rand.Reader,
		sign: func(claims jwt.Claims) (string, error) {
			return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(key)
		},
	}
}

// Generate returns a signed token and its expiry.
func (m *TokenManager) Generate(p domain.Principal) (string, time.Time, error) {
	jti, err := randomHex(m.random, jtiBytes)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("generate access token id: %w", err)
	}
	now := m.now()
	expiresAt := now.Add(m.ttl)

	claims := jwt.MapClaims{
		"jti":     jti,
		"sub":     p.UserID,
		"club_id": p.ClubID,
		"role":    string(p.Role),
		"iss":     m.issuer,
		"iat":     now.Unix(),
		"exp":     expiresAt.Unix(),
	}
	signed, err := m.sign(claims)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign access token: %w", err)
	}
	return signed, expiresAt, nil
}

// Verify rejects any token that is not a valid, unexpired HS256 token from this
// issuer with a well-formed principal. Every failure maps to ErrUnauthorized.
func (m *TokenManager) Verify(raw string) (domain.Principal, error) {
	claims := jwt.MapClaims{}
	token, err := jwt.ParseWithClaims(raw, claims,
		func(*jwt.Token) (any, error) { return m.secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(m.issuer),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(m.now),
	)
	if err != nil || !token.Valid {
		return domain.Principal{}, domain.ErrUnauthorized
	}

	principal := domain.Principal{
		UserID: stringClaim(claims, "sub"),
		ClubID: stringClaim(claims, "club_id"),
		Role:   domain.Role(stringClaim(claims, "role")),
	}
	if !domain.IsUUID(principal.UserID) || !domain.IsUUID(principal.ClubID) || !principal.Role.Valid() {
		return domain.Principal{}, domain.ErrUnauthorized
	}
	return principal, nil
}

func stringClaim(claims jwt.MapClaims, key string) string {
	value, _ := claims[key].(string)
	return value
}
