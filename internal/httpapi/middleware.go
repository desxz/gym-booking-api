package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/muratgun/gym-booking-api/internal/auth"
	"github.com/muratgun/gym-booking-api/internal/domain"
)

const (
	HeaderRequestID     = "X-Request-ID"
	HeaderAuthorization = "Authorization"
	MaxRequestIDLen     = 64

	headerContentTypeOptions = "X-Content-Type-Options"
	headerFrameOptions       = "X-Frame-Options"
	headerCacheControl       = "Cache-Control"
	contentTypeNoSniff       = "nosniff"
	frameOptionsDeny         = "DENY"
	cacheControlNoStore      = "no-store"
	requestIDFallback        = "request-id-unavailable"
	requestIDBytes           = 16
	minPrintableASCII        = 0x20
	maxPrintableASCII        = 0x7E
)

// requestIDRandom is a package seam so the rare crypto/rand failure path can be
// tested deterministically.
var requestIDRandom io.Reader = rand.Reader

// Middleware is the standard http.Handler decorator shape used by chain.
type Middleware func(http.Handler) http.Handler

// chain applies middleware so the first one listed is the outermost.
func chain(h http.Handler, middlewares ...Middleware) http.Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		h = middlewares[i](h)
	}
	return h
}

//go:generate go run go.uber.org/mock/mockgen -source=middleware.go -destination=mock_middleware_deps_test.go -package=httpapi

// TokenVerifier is the HTTP layer's view of the JWT verifier.
type TokenVerifier interface {
	Verify(raw string) (domain.Principal, error)
}

type requestIDKey struct{}
type requestInfoKey struct{}

// requestInfo is mutable request-scoped data for logging: the authenticated
// identity is filled in by RequireAuth, which runs after AccessLog has started.
type requestInfo struct {
	userID string
	clubID string
}

func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(headerContentTypeOptions, contentTypeNoSniff)
		w.Header().Set(headerFrameOptions, frameOptionsDeny)
		w.Header().Set(headerCacheControl, cacheControlNoStore)
		next.ServeHTTP(w, r)
	})
}

// RequestID accepts a safe client correlation ID or generates one, then exposes
// it through the response header and the request context.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := sanitizeRequestID(r.Header.Get(HeaderRequestID))
		if id == "" {
			generated, err := newRequestID()
			if err != nil {
				generated = requestIDFallback
			}
			id = generated
		}
		w.Header().Set(HeaderRequestID, id)

		ctx := context.WithValue(r.Context(), requestIDKey{}, id)
		ctx = context.WithValue(ctx, requestInfoKey{}, &requestInfo{})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

func sanitizeRequestID(value string) string {
	if !validHeaderValue(value, MaxRequestIDLen) {
		return ""
	}
	return value
}

func validHeaderValue(value string, maxLen int) bool {
	if len(value) == 0 || len(value) > maxLen {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < minPrintableASCII || value[i] > maxPrintableASCII {
			return false
		}
	}
	return true
}

func newRequestID() (string, error) {
	var buf [requestIDBytes]byte
	if _, err := io.ReadFull(requestIDRandom, buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// AccessLog records one structured line per request. It never logs headers,
// bodies, passwords, or tokens.
func AccessLog(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(recorder, r)

			attrs := []any{
				"method", r.Method,
				"path", r.URL.Path,
				"status", recorder.status,
				"duration_ms", time.Since(start).Milliseconds(),
				"request_id", RequestIDFromContext(r.Context()),
			}
			if info, ok := r.Context().Value(requestInfoKey{}).(*requestInfo); ok && info.userID != "" {
				attrs = append(attrs, "user_id", info.userID, "club_id", info.clubID)
			}
			logger.Info("request completed", attrs...)
		})
	}
}

// Recover sits inside AccessLog so the access log records the recovered 500.
func Recover(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				recovered := recover()
				if recovered == nil {
					return
				}
				requestID := RequestIDFromContext(r.Context())
				logger.Error("panic recovered",
					"request_id", requestID,
					"panic", recovered,
					"stack", string(debug.Stack()),
				)
				writeJSON(w, http.StatusInternalServerError, errorResponse{
					Code:      domain.CodeInternal,
					Message:   messageInternal,
					RequestID: requestID,
				})
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// RequestTimeout bounds handler work with a context deadline; it deliberately
// avoids http.TimeoutHandler so error responses keep the API JSON shape.
func RequestTimeout(timeout time.Duration) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), timeout)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func RequireAuth(tokens TokenVerifier, logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, ok := bearerToken(r.Header.Get(HeaderAuthorization))
			if !ok {
				writeError(w, r, logger, domain.ErrUnauthorized)
				return
			}
			principal, err := tokens.Verify(raw)
			if err != nil {
				writeError(w, r, logger, domain.ErrUnauthorized)
				return
			}
			if info, ok := r.Context().Value(requestInfoKey{}).(*requestInfo); ok {
				info.userID = principal.UserID
				info.clubID = principal.ClubID
			}
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), principal)))
		})
	}
}

func RequireRole(role domain.Role, logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := auth.PrincipalFromContext(r.Context())
			if !ok {
				writeError(w, r, logger, domain.ErrUnauthorized)
				return
			}
			if principal.Role != role {
				writeError(w, r, logger, domain.ErrForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func bearerToken(header string) (string, bool) {
	if len(header) <= len(auth.BearerPrefix) || !strings.EqualFold(header[:len(auth.BearerPrefix)], auth.BearerPrefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(auth.BearerPrefix):])
	return token, token != ""
}
