package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/muratgun/gym-booking-api/internal/domain"
)

const (
	EventRefreshTokenReuse = "refresh_token_reuse_detected"

	statusOK       = "ok"
	statusReady    = "ready"
	statusNotReady = "not_ready"
)

type RouterConfig struct {
	Auth             AuthService
	Bookings         BookingService
	Tokens           TokenVerifier
	Logger           *slog.Logger
	RequestTimeout   time.Duration
	MaxBodyBytes     int64
	ReadyPingTimeout time.Duration
	ReadyCheck       func(ctx context.Context) error
}

// NewRouter is where every request enters the API and where the middleware
// stack is composed. Health probes get their own short timeout; API routes are
// bounded by RequestTimeout.
func NewRouter(cfg RouterConfig) http.Handler {
	handler := &Handler{
		auth:             cfg.Auth,
		bookings:         cfg.Bookings,
		logger:           cfg.Logger,
		ready:            cfg.ReadyCheck,
		maxBodyBytes:     cfg.MaxBodyBytes,
		readyPingTimeout: cfg.ReadyPingTimeout,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handler.route(handler.health))
	mux.HandleFunc("GET /readyz", handler.route(handler.readiness))

	api := http.NewServeMux()
	api.HandleFunc("POST /api/v1/auth/register", handler.route(handler.register))
	api.HandleFunc("POST /api/v1/auth/login", handler.route(handler.login))
	api.HandleFunc("POST /api/v1/auth/refresh", handler.route(handler.refresh))
	api.Handle("POST /api/v1/auth/logout", chain(
		handler.route(handler.logout),
		RequireAuth(cfg.Tokens, cfg.Logger),
	))
	api.Handle("GET /api/v1/classes", chain(
		handler.route(handler.listClasses),
		RequireAuth(cfg.Tokens, cfg.Logger),
	))
	api.Handle("POST /api/v1/classes", chain(
		handler.route(handler.createClass),
		RequireAuth(cfg.Tokens, cfg.Logger),
		RequireRole(domain.RoleTrainer, cfg.Logger),
	))
	api.Handle("POST /api/v1/classes/{id}/bookings", chain(
		handler.route(handler.bookClass),
		RequireAuth(cfg.Tokens, cfg.Logger),
		RequireRole(domain.RoleMember, cfg.Logger),
	))
	api.Handle("DELETE /api/v1/bookings/{id}", chain(
		handler.route(handler.cancelBooking),
		RequireAuth(cfg.Tokens, cfg.Logger),
	))

	mux.Handle("/api/v1/", RequestTimeout(cfg.RequestTimeout)(api))

	return chain(mux,
		SecurityHeaders,
		RequestID,
		AccessLog(cfg.Logger),
		Recover(cfg.Logger),
	)
}
