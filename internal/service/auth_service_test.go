package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/muratgun/gym-booking-api/internal/domain"
)

var (
	serviceNow = time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	clubAID    = "11111111-1111-1111-1111-111111111111"
	memberAID  = "11111111-1111-1111-1111-111111111102"
)

type authMocks struct {
	users         *MockUserRepository
	refreshTokens *MockRefreshTokenRepository
	hasher        *MockPasswordHasher
	accessTokens  *MockTokenIssuer
	refreshIssuer *MockRefreshTokenIssuer
}

func newAuthDeps(ctrl *gomock.Controller) (AuthDeps, authMocks) {
	m := authMocks{
		users:         NewMockUserRepository(ctrl),
		refreshTokens: NewMockRefreshTokenRepository(ctrl),
		hasher:        NewMockPasswordHasher(ctrl),
		accessTokens:  NewMockTokenIssuer(ctrl),
		refreshIssuer: NewMockRefreshTokenIssuer(ctrl),
	}
	return AuthDeps{
		Users:              m.users,
		RefreshTokens:      m.refreshTokens,
		Hasher:             m.hasher,
		AccessTokens:       m.accessTokens,
		RefreshTokenIssuer: m.refreshIssuer,
		DummyPasswordHash:  "dummy-hash",
		Clock:              func() time.Time { return serviceNow },
	}, m
}

func storedUser() domain.User {
	return domain.User{
		ID:           memberAID,
		ClubID:       clubAID,
		Email:        "member@example.com",
		PasswordHash: "stored-hash",
		Role:         domain.RoleMember,
	}
}

func TestNewAuthServiceClock(t *testing.T) {
	ctrl := gomock.NewController(t)
	deps, _ := newAuthDeps(ctrl)

	service := NewAuthService(deps)
	assert.Equal(t, serviceNow, service.now())

	defaultClock := NewAuthService(AuthDeps{})
	assert.False(t, defaultClock.now().IsZero())
	assert.Equal(t, time.UTC, defaultClock.now().Location())
}

func TestRegister(t *testing.T) {
	t.Run("rejects a non-UUID club", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		deps, _ := newAuthDeps(ctrl)

		_, err := NewAuthService(deps).Register(context.Background(), "club", "new@example.com", "Password123!")

		var validation *domain.ValidationError
		require.ErrorAs(t, err, &validation)
		assert.Contains(t, validation.Fields, "club_id")
	})

	t.Run("rejects an invalid email", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		deps, _ := newAuthDeps(ctrl)

		_, err := NewAuthService(deps).Register(context.Background(), clubAID, "not-an-email", "Password123!")

		var validation *domain.ValidationError
		require.ErrorAs(t, err, &validation)
		assert.Contains(t, validation.Fields, "email")
	})

	t.Run("propagates hashing errors", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		deps, m := newAuthDeps(ctrl)
		hashErr := errors.New("hash failed")
		m.hasher.EXPECT().HashPassword("Password123!").Return("", hashErr).Times(1)

		_, err := NewAuthService(deps).Register(context.Background(), clubAID, "new@example.com", "Password123!")
		require.ErrorIs(t, err, hashErr)
	})

	t.Run("propagates repository errors", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		deps, m := newAuthDeps(ctrl)
		repoErr := errors.New("insert failed")
		m.hasher.EXPECT().HashPassword("Password123!").Return("hashed", nil).Times(1)
		m.users.EXPECT().CreateUser(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, user domain.User) (domain.User, error) {
				assert.Equal(t, "new@example.com", user.Email)
				assert.Equal(t, domain.RoleMember, user.Role)
				assert.Equal(t, "hashed", user.PasswordHash)
				return domain.User{}, repoErr
			}).Times(1)

		_, err := NewAuthService(deps).Register(context.Background(), clubAID, "new@example.com", "Password123!")
		require.ErrorIs(t, err, repoErr)
	})

	t.Run("normalizes email and forces the member role", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		deps, m := newAuthDeps(ctrl)
		m.hasher.EXPECT().HashPassword("Password123!").Return("hash-of-password", nil).Times(1)
		m.users.EXPECT().CreateUser(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, user domain.User) (domain.User, error) {
				assert.Equal(t, clubAID, user.ClubID)
				assert.Equal(t, "new@example.com", user.Email)
				assert.Equal(t, domain.RoleMember, user.Role)
				assert.Equal(t, "hash-of-password", user.PasswordHash)
				user.ID = "created-id"
				return user, nil
			}).Times(1)

		user, err := NewAuthService(deps).Register(context.Background(), clubAID, "  New@Example.COM  ", "Password123!")
		require.NoError(t, err)
		assert.Equal(t, "created-id", user.ID)
	})
}

func TestLogin(t *testing.T) {
	t.Run("invalid email is invalid credentials", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		deps, _ := newAuthDeps(ctrl)

		_, err := NewAuthService(deps).Login(context.Background(), "nope", "Password123!")
		require.ErrorIs(t, err, domain.ErrInvalidCredentials)
	})

	t.Run("unknown user still verifies the dummy hash", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		deps, m := newAuthDeps(ctrl)
		m.users.EXPECT().UserByEmail(gomock.Any(), "missing@example.com").
			Return(domain.User{}, domain.ErrNotFound).Times(1)
		m.hasher.EXPECT().VerifyPassword("dummy-hash", "Password123!").
			Return(errors.New("mismatch")).Times(1)

		_, err := NewAuthService(deps).Login(context.Background(), "missing@example.com", "Password123!")
		require.ErrorIs(t, err, domain.ErrInvalidCredentials)
	})

	t.Run("propagates lookup errors", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		deps, m := newAuthDeps(ctrl)
		repoErr := errors.New("lookup failed")
		m.users.EXPECT().UserByEmail(gomock.Any(), "user@example.com").
			Return(domain.User{}, repoErr).Times(1)

		_, err := NewAuthService(deps).Login(context.Background(), "user@example.com", "Password123!")
		require.ErrorIs(t, err, repoErr)
	})

	t.Run("wrong password is invalid credentials", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		deps, m := newAuthDeps(ctrl)
		m.users.EXPECT().UserByEmail(gomock.Any(), "user@example.com").Return(storedUser(), nil).Times(1)
		m.hasher.EXPECT().VerifyPassword("stored-hash", "wrong-password").
			Return(errors.New("mismatch")).Times(1)

		_, err := NewAuthService(deps).Login(context.Background(), "user@example.com", "wrong-password")
		require.ErrorIs(t, err, domain.ErrInvalidCredentials)
	})

	t.Run("access token generation error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		deps, m := newAuthDeps(ctrl)
		tokenErr := errors.New("token failed")
		expectSuccessfulLogin(m)
		m.accessTokens.EXPECT().Generate(gomock.Eq(testPrincipal())).Return("", time.Time{}, tokenErr).Times(1)

		_, err := NewAuthService(deps).Login(context.Background(), "user@example.com", "Password123!")
		require.ErrorIs(t, err, tokenErr)
	})

	t.Run("refresh token generation error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		deps, m := newAuthDeps(ctrl)
		refreshErr := errors.New("refresh failed")
		expectSuccessfulLogin(m)
		m.accessTokens.EXPECT().Generate(gomock.Eq(testPrincipal())).
			Return("access-token", serviceNow.Add(15*time.Minute), nil).Times(1)
		m.refreshIssuer.EXPECT().Generate().Return("", "", time.Time{}, refreshErr).Times(1)

		_, err := NewAuthService(deps).Login(context.Background(), "user@example.com", "Password123!")
		require.ErrorIs(t, err, refreshErr)
	})

	t.Run("refresh token persistence error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		deps, m := newAuthDeps(ctrl)
		persistErr := errors.New("insert failed")
		expectSuccessfulLogin(m)
		expectSessionTokens(t, m, persistErr)

		_, err := NewAuthService(deps).Login(context.Background(), "user@example.com", "Password123!")
		require.ErrorIs(t, err, persistErr)
	})

	t.Run("success returns both tokens and persists only the hash", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		deps, m := newAuthDeps(ctrl)
		expectSuccessfulLogin(m)
		expectSessionTokens(t, m, nil)

		session, err := NewAuthService(deps).Login(context.Background(), "user@example.com", "Password123!")
		require.NoError(t, err)
		assert.Equal(t, "access-token", session.AccessToken)
		assert.Equal(t, "raw-refresh", session.RefreshToken)
		assert.Equal(t, serviceNow.Add(15*time.Minute), session.ExpiresAt)
	})
}

func expectSuccessfulLogin(m authMocks) {
	m.users.EXPECT().UserByEmail(gomock.Any(), "user@example.com").Return(storedUser(), nil).Times(1)
	m.hasher.EXPECT().VerifyPassword("stored-hash", "Password123!").Return(nil).Times(1)
}

func expectSessionTokens(t *testing.T, m authMocks, createErr error) {
	t.Helper()
	m.accessTokens.EXPECT().Generate(gomock.Eq(testPrincipal())).
		Return("access-token", serviceNow.Add(15*time.Minute), nil).Times(1)
	m.refreshIssuer.EXPECT().Generate().
		Return("raw-refresh", "refresh-hash", serviceNow.Add(7*24*time.Hour), nil).Times(1)
	m.refreshTokens.EXPECT().CreateRefreshToken(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, token domain.RefreshToken) error {
			assert.Equal(t, memberAID, token.UserID)
			assert.Equal(t, clubAID, token.ClubID)
			assert.Equal(t, "refresh-hash", token.TokenHash)
			assert.NotEqual(t, "raw-refresh", token.TokenHash)
			return createErr
		}).Times(1)
}

func testPrincipal() domain.Principal {
	return domain.Principal{UserID: memberAID, ClubID: clubAID, Role: domain.RoleMember}
}

func TestRefresh(t *testing.T) {
	t.Run("empty token is unauthorized", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		deps, _ := newAuthDeps(ctrl)

		_, err := NewAuthService(deps).Refresh(context.Background(), "")
		require.ErrorIs(t, err, domain.ErrUnauthorized)
	})

	t.Run("new refresh token generation error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		deps, m := newAuthDeps(ctrl)
		generateErr := errors.New("generate failed")
		m.refreshIssuer.EXPECT().Generate().Return("", "", time.Time{}, generateErr).Times(1)

		_, err := NewAuthService(deps).Refresh(context.Background(), "old")
		require.ErrorIs(t, err, generateErr)
	})

	t.Run("rotation errors propagate", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		deps, m := newAuthDeps(ctrl)
		reuseErr := &domain.RefreshTokenReuseError{Principal: testPrincipal(), FamilyID: "family"}
		m.refreshIssuer.EXPECT().Generate().
			Return("new-raw", "new-hash", serviceNow.Add(7*24*time.Hour), nil).Times(1)
		m.refreshIssuer.EXPECT().Hash("old").Return("hash:old").Times(1)
		m.refreshTokens.EXPECT().
			RotateRefreshToken(gomock.Any(), "hash:old", serviceNow, "new-hash", serviceNow.Add(7*24*time.Hour)).
			Return(domain.Principal{}, reuseErr).Times(1)

		_, err := NewAuthService(deps).Refresh(context.Background(), "old")
		require.ErrorIs(t, err, reuseErr)
	})

	t.Run("access token generation error after rotation", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		deps, m := newAuthDeps(ctrl)
		tokenErr := errors.New("access failed")
		m.refreshIssuer.EXPECT().Generate().
			Return("new-raw", "new-hash", serviceNow.Add(7*24*time.Hour), nil).Times(1)
		m.refreshIssuer.EXPECT().Hash("old").Return("hash:old").Times(1)
		m.refreshTokens.EXPECT().
			RotateRefreshToken(gomock.Any(), "hash:old", serviceNow, "new-hash", serviceNow.Add(7*24*time.Hour)).
			Return(testPrincipal(), nil).Times(1)
		m.accessTokens.EXPECT().Generate(gomock.Eq(testPrincipal())).Return("", time.Time{}, tokenErr).Times(1)

		_, err := NewAuthService(deps).Refresh(context.Background(), "old")
		require.ErrorIs(t, err, tokenErr)
	})

	t.Run("success rotates with the hash and injected clock", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		deps, m := newAuthDeps(ctrl)
		refreshExpiry := serviceNow.Add(7 * 24 * time.Hour)
		m.refreshIssuer.EXPECT().Generate().
			Return("new-raw", "new-hash", refreshExpiry, nil).Times(1)
		m.refreshIssuer.EXPECT().Hash("old-raw").Return("hash:old-raw").Times(1)
		m.refreshTokens.EXPECT().
			RotateRefreshToken(gomock.Any(), "hash:old-raw", serviceNow, "new-hash", refreshExpiry).
			Return(testPrincipal(), nil).Times(1)
		m.accessTokens.EXPECT().Generate(gomock.Eq(testPrincipal())).
			Return("new-access", serviceNow.Add(15*time.Minute), nil).Times(1)

		session, err := NewAuthService(deps).Refresh(context.Background(), "old-raw")
		require.NoError(t, err)
		assert.Equal(t, "new-access", session.AccessToken)
		assert.Equal(t, "new-raw", session.RefreshToken)
		assert.Equal(t, serviceNow.Add(15*time.Minute), session.ExpiresAt)
	})
}

func TestLogout(t *testing.T) {
	t.Run("empty token is unauthorized", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		deps, _ := newAuthDeps(ctrl)

		err := NewAuthService(deps).Logout(context.Background(), testPrincipal(), "")
		require.ErrorIs(t, err, domain.ErrUnauthorized)
	})

	t.Run("repository errors propagate", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		deps, m := newAuthDeps(ctrl)
		repoErr := errors.New("revoke failed")
		m.refreshIssuer.EXPECT().Hash("raw").Return("hash:raw").Times(1)
		m.refreshTokens.EXPECT().
			RevokeRefreshFamily(gomock.Any(), "hash:raw", gomock.Eq(testPrincipal()), serviceNow).
			Return(repoErr).Times(1)

		err := NewAuthService(deps).Logout(context.Background(), testPrincipal(), "raw")
		require.ErrorIs(t, err, repoErr)
	})

	t.Run("revokes by hash with the actor and clock", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		deps, m := newAuthDeps(ctrl)
		m.refreshIssuer.EXPECT().Hash("raw").Return("hash:raw").Times(1)
		m.refreshTokens.EXPECT().
			RevokeRefreshFamily(gomock.Any(), "hash:raw", gomock.Eq(testPrincipal()), serviceNow).
			Return(nil).Times(1)

		require.NoError(t, NewAuthService(deps).Logout(context.Background(), testPrincipal(), "raw"))
	})
}
