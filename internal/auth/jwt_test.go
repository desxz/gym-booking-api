package auth

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/muratgun/gym-booking-api/internal/config"
	"github.com/muratgun/gym-booking-api/internal/domain"
)

const testSecret32 = "01234567890123456789012345678901"

func newTestTokenManager(t *testing.T) *TokenManager {
	t.Helper()
	manager := NewTokenManager("gym-booking-api", 15*time.Minute, config.Secret(testSecret32))
	manager.now = func() time.Time { return authNow }
	return manager
}

func testPrincipal() domain.Principal {
	return domain.Principal{
		UserID: "11111111-1111-1111-1111-111111111102",
		ClubID: "11111111-1111-1111-1111-111111111111",
		Role:   domain.RoleMember,
	}
}

func signClaims(t *testing.T, method jwt.SigningMethod, key any, claims jwt.MapClaims) string {
	t.Helper()
	token, err := jwt.NewWithClaims(method, claims).SignedString(key)
	require.NoError(t, err)
	return token
}

func TestTokenManagerGenerate(t *testing.T) {
	manager := newTestTokenManager(t)
	token, expiresAt, err := manager.Generate(testPrincipal())
	require.NoError(t, err)
	assert.Equal(t, authNow.Add(15*time.Minute), expiresAt)

	parsed, err := jwt.Parse(token, func(*jwt.Token) (any, error) {
		return []byte(testSecret32), nil
	}, jwt.WithoutClaimsValidation())
	require.NoError(t, err)
	require.True(t, parsed.Valid)

	claims, ok := parsed.Claims.(jwt.MapClaims)
	require.True(t, ok)
	jti, _ := claims["jti"].(string)
	decodedJTI, err := hex.DecodeString(jti)
	require.NoError(t, err)
	assert.Len(t, jti, 32)
	assert.Len(t, decodedJTI, 16)
	assert.Equal(t, testPrincipal().UserID, claims["sub"])
	assert.Equal(t, testPrincipal().ClubID, claims["club_id"])
	assert.Equal(t, string(domain.RoleMember), claims["role"])
	assert.Equal(t, "gym-booking-api", claims["iss"])
	assert.Equal(t, float64(authNow.Unix()), claims["iat"])
	assert.Equal(t, float64(expiresAt.Unix()), claims["exp"])
}

func TestTokenManagerGenerateErrors(t *testing.T) {
	t.Run("random source failure", func(t *testing.T) {
		manager := newTestTokenManager(t)
		manager.random = failingReader{}
		_, _, err := manager.Generate(testPrincipal())
		require.Error(t, err)
	})

	t.Run("signer failure", func(t *testing.T) {
		manager := newTestTokenManager(t)
		manager.sign = func(jwt.Claims) (string, error) { return "", errors.New("signer failed") }
		_, _, err := manager.Generate(testPrincipal())
		require.Error(t, err)
	})
}

func TestTokenManagerVerify(t *testing.T) {
	manager := newTestTokenManager(t)
	token, _, err := manager.Generate(testPrincipal())
	require.NoError(t, err)

	principal, err := manager.Verify(token)
	require.NoError(t, err)
	assert.Equal(t, testPrincipal(), principal)
}

func TestTokenManagerVerifyFailures(t *testing.T) {
	manager := newTestTokenManager(t)
	validToken, _, err := manager.Generate(testPrincipal())
	require.NoError(t, err)

	otherIssuer := NewTokenManager("other-issuer", 15*time.Minute, config.Secret(testSecret32))
	otherIssuer.now = manager.now
	otherIssuerToken, _, err := otherIssuer.Generate(testPrincipal())
	require.NoError(t, err)

	otherSecret := NewTokenManager("gym-booking-api", 15*time.Minute, config.Secret("another-secret-that-is-32-bytes!!"))
	otherSecret.now = manager.now
	otherSecretToken, _, err := otherSecret.Generate(testPrincipal())
	require.NoError(t, err)

	baseClaims := jwt.MapClaims{
		"sub":     testPrincipal().UserID,
		"club_id": testPrincipal().ClubID,
		"role":    string(domain.RoleMember),
		"iss":     "gym-booking-api",
		"iat":     authNow.Unix(),
		"exp":     authNow.Add(time.Hour).Unix(),
	}
	withClaim := func(key string, value any) jwt.MapClaims {
		claims := jwt.MapClaims{}
		for k, v := range baseClaims {
			claims[k] = v
		}
		claims[key] = value
		return claims
	}
	deleteExpiry := jwt.MapClaims{}
	for k, v := range baseClaims {
		if k != "exp" {
			deleteExpiry[k] = v
		}
	}

	tests := []struct {
		name  string
		token string
	}{
		{"malformed", "not-a-jwt"},
		{"bad signature", otherSecretToken},
		{"wrong issuer", otherIssuerToken},
		{"wrong algorithm", signClaims(t, jwt.SigningMethodHS384, []byte(testSecret32), baseClaims)},
		{"missing expiration", signClaims(t, jwt.SigningMethodHS256, []byte(testSecret32), deleteExpiry)},
		{"non-uuid subject", signClaims(t, jwt.SigningMethodHS256, []byte(testSecret32), withClaim("sub", "not-a-uuid"))},
		{"non-string subject", signClaims(t, jwt.SigningMethodHS256, []byte(testSecret32), withClaim("sub", 42))},
		{"non-uuid club", signClaims(t, jwt.SigningMethodHS256, []byte(testSecret32), withClaim("club_id", "club"))},
		{"unknown role", signClaims(t, jwt.SigningMethodHS256, []byte(testSecret32), withClaim("role", "admin"))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := manager.Verify(tt.token)
			require.ErrorIs(t, err, domain.ErrUnauthorized)
		})
	}

	t.Run("expired", func(t *testing.T) {
		manager.now = func() time.Time { return authNow.Add(16 * time.Minute) }
		_, err := manager.Verify(validToken)
		require.ErrorIs(t, err, domain.ErrUnauthorized)
	})
}

func TestBearerConstants(t *testing.T) {
	assert.Equal(t, "Bearer ", BearerPrefix)
	assert.Equal(t, "Bearer", TokenTypeBearer)
	assert.True(t, strings.HasPrefix(TokenTypeBearer, strings.TrimSpace(BearerPrefix)))
}

func TestDefaultConstructorsUseRealClockAndRandomness(t *testing.T) {
	manager := NewTokenManager("gym-booking-api", time.Hour, config.Secret(testSecret32))
	token, _, err := manager.Generate(testPrincipal())
	require.NoError(t, err)
	_, err = manager.Verify(token)
	require.NoError(t, err)

	parsed, err := jwt.Parse(token, func(*jwt.Token) (any, error) {
		return []byte(testSecret32), nil
	}, jwt.WithoutClaimsValidation())
	require.NoError(t, err)
	claims, ok := parsed.Claims.(jwt.MapClaims)
	require.True(t, ok)
	issuedAt, exp := claims["iat"].(float64), claims["exp"].(float64)
	assert.Equal(t, float64(3600), exp-issuedAt)

	refresh := NewRefreshTokenManager(time.Hour)
	raw, hash, expiresAt, err := refresh.Generate()
	require.NoError(t, err)
	assert.NotEmpty(t, raw)
	assert.Equal(t, HashRefreshToken(raw), hash)
	assert.Equal(t, time.UTC, expiresAt.Location())
	assert.False(t, expiresAt.IsZero())
}
