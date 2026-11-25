package auth

import (
	"context"

	"github.com/muratgun/gym-booking-api/internal/domain"
)

type principalKey struct{}

// WithPrincipal returns a child context carrying the authenticated identity.
func WithPrincipal(ctx context.Context, p domain.Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFromContext returns the identity set by RequireAuth.
func PrincipalFromContext(ctx context.Context) (domain.Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(domain.Principal)
	return p, ok
}
