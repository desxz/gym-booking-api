package service

import (
	"context"
	"errors"
	"net/mail"
	"strings"
	"time"

	"github.com/muratgun/gym-booking-api/internal/domain"
)

//go:generate go run go.uber.org/mock/mockgen -source=auth_service.go -destination=mock_auth_deps_test.go -package=service

// Consumer-side interfaces keep the services independent of pgx, JWT, and bcrypt.
type UserRepository interface {
	CreateUser(ctx context.Context, user domain.User) (domain.User, error)
	UserByEmail(ctx context.Context, email string) (domain.User, error)
}

type RefreshTokenRepository interface {
	CreateRefreshToken(ctx context.Context, token domain.RefreshToken) error
	RotateRefreshToken(ctx context.Context, presentedHash string, now time.Time, replacementHash string, replacementExpiresAt time.Time) (domain.Principal, error)
	RevokeRefreshFamily(ctx context.Context, presentedHash string, actor domain.Principal, now time.Time) error
}

type PasswordHasher interface {
	HashPassword(plain string) (string, error)
	VerifyPassword(hash, plain string) error
}

type TokenIssuer interface {
	Generate(p domain.Principal) (string, time.Time, error)
}

type RefreshTokenIssuer interface {
	Generate() (raw string, hash string, expiresAt time.Time, err error)
	Hash(raw string) string
}

type AuthDeps struct {
	Users              UserRepository
	RefreshTokens      RefreshTokenRepository
	Hasher             PasswordHasher
	AccessTokens       TokenIssuer
	RefreshTokenIssuer RefreshTokenIssuer
	DummyPasswordHash  string
	Clock              func() time.Time
}

type AuthService struct {
	users         UserRepository
	refreshTokens RefreshTokenRepository
	hasher        PasswordHasher
	accessTokens  TokenIssuer
	refreshIssuer RefreshTokenIssuer
	dummyHash     string
	now           func() time.Time
}

func NewAuthService(deps AuthDeps) *AuthService {
	clock := deps.Clock
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	return &AuthService{
		users:         deps.Users,
		refreshTokens: deps.RefreshTokens,
		hasher:        deps.Hasher,
		accessTokens:  deps.AccessTokens,
		refreshIssuer: deps.RefreshTokenIssuer,
		dummyHash:     deps.DummyPasswordHash,
		now:           clock,
	}
}

// Register creates a member account in the given club. The role is never taken
// from the caller: self-registration can only produce members.
func (s *AuthService) Register(ctx context.Context, clubID, email, password string) (domain.User, error) {
	if !domain.IsUUID(clubID) {
		return domain.User{}, &domain.ValidationError{Fields: map[string]string{
			"club_id": "must be a valid UUID",
		}}
	}
	normalized, err := normalizeEmail(email)
	if err != nil {
		return domain.User{}, err
	}
	hash, err := s.hasher.HashPassword(password)
	if err != nil {
		return domain.User{}, err
	}

	return s.users.CreateUser(ctx, domain.User{
		ClubID:       clubID,
		Email:        normalized,
		PasswordHash: hash,
		Role:         domain.RoleMember,
	})
}

// Login returns an access/refresh pair. Unknown users are still verified against
// the dummy hash so both failure paths pay the same bcrypt cost.
func (s *AuthService) Login(ctx context.Context, email, password string) (domain.Session, error) {
	normalized, err := normalizeEmail(email)
	if err != nil {
		return domain.Session{}, domain.ErrInvalidCredentials
	}

	user, err := s.users.UserByEmail(ctx, normalized)
	if errors.Is(err, domain.ErrNotFound) {
		_ = s.hasher.VerifyPassword(s.dummyHash, password)
		return domain.Session{}, domain.ErrInvalidCredentials
	}
	if err != nil {
		return domain.Session{}, err
	}
	if err := s.hasher.VerifyPassword(user.PasswordHash, password); err != nil {
		return domain.Session{}, domain.ErrInvalidCredentials
	}

	return s.issueSession(ctx, domain.Principal{
		UserID: user.ID,
		ClubID: user.ClubID,
		Role:   user.Role,
	})
}

// Refresh rotates an opaque refresh token and returns a new access/refresh pair.
func (s *AuthService) Refresh(ctx context.Context, rawRefreshToken string) (domain.Session, error) {
	if rawRefreshToken == "" {
		return domain.Session{}, domain.ErrUnauthorized
	}

	newRaw, newHash, refreshExpiresAt, err := s.refreshIssuer.Generate()
	if err != nil {
		return domain.Session{}, err
	}
	principal, err := s.refreshTokens.RotateRefreshToken(
		ctx, s.refreshIssuer.Hash(rawRefreshToken), s.now(), newHash, refreshExpiresAt)
	if err != nil {
		return domain.Session{}, err
	}

	accessToken, accessExpiresAt, err := s.accessTokens.Generate(principal)
	if err != nil {
		return domain.Session{}, err
	}
	return domain.Session{
		AccessToken:  accessToken,
		RefreshToken: newRaw,
		ExpiresAt:    accessExpiresAt,
	}, nil
}

// Logout revokes the whole refresh-token family; unknown tokens are a no-op.
func (s *AuthService) Logout(ctx context.Context, actor domain.Principal, rawRefreshToken string) error {
	if rawRefreshToken == "" {
		return domain.ErrUnauthorized
	}
	return s.refreshTokens.RevokeRefreshFamily(ctx, s.refreshIssuer.Hash(rawRefreshToken), actor, s.now())
}

func (s *AuthService) issueSession(ctx context.Context, principal domain.Principal) (domain.Session, error) {
	accessToken, accessExpiresAt, err := s.accessTokens.Generate(principal)
	if err != nil {
		return domain.Session{}, err
	}
	rawRefreshToken, refreshHash, refreshExpiresAt, err := s.refreshIssuer.Generate()
	if err != nil {
		return domain.Session{}, err
	}
	if err := s.refreshTokens.CreateRefreshToken(ctx, domain.RefreshToken{
		UserID:    principal.UserID,
		ClubID:    principal.ClubID,
		TokenHash: refreshHash,
		ExpiresAt: refreshExpiresAt,
	}); err != nil {
		return domain.Session{}, err
	}
	return domain.Session{
		AccessToken:  accessToken,
		RefreshToken: rawRefreshToken,
		ExpiresAt:    accessExpiresAt,
	}, nil
}

func normalizeEmail(raw string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(raw))
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email {
		return "", &domain.ValidationError{Fields: map[string]string{
			"email": "must be a valid email address",
		}}
	}
	return email, nil
}
