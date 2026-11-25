package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/muratgun/gym-booking-api/internal/auth"
	"github.com/muratgun/gym-booking-api/internal/domain"
)

const (
	HeaderIdempotencyKey = "Idempotency-Key"
	MaxIdempotencyKeyLen = 128
)

//go:generate go run go.uber.org/mock/mockgen -source=handler.go -destination=mock_handler_deps_test.go -package=httpapi

// AuthService is the HTTP layer's consumer-side view of authentication.
type AuthService interface {
	Register(ctx context.Context, clubID, email, password string) (domain.User, error)
	Login(ctx context.Context, email, password string) (domain.Session, error)
	Refresh(ctx context.Context, refreshToken string) (domain.Session, error)
	Logout(ctx context.Context, actor domain.Principal, refreshToken string) error
}

// BookingService is the HTTP layer's consumer-side view of bookings.
type BookingService interface {
	CreateClass(ctx context.Context, actor domain.Principal, title string, startsAt time.Time, capacity int) (domain.Class, error)
	ListClasses(ctx context.Context, actor domain.Principal) ([]domain.Class, error)
	BookClass(ctx context.Context, actor domain.Principal, classID, idempotencyKey string) (domain.Booking, error)
	CancelBooking(ctx context.Context, actor domain.Principal, bookingID string) error
}

type Handler struct {
	auth             AuthService
	bookings         BookingService
	logger           *slog.Logger
	ready            func(context.Context) error
	maxBodyBytes     int64
	readyPingTimeout time.Duration
}

// route adapts an error-returning handler to net/http so every failure goes
// through the single writeError exit.
func (h *Handler) route(fn func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := fn(w, r); err != nil {
			writeError(w, r, h.logger, err)
		}
	}
}

func (h *Handler) health(w http.ResponseWriter, _ *http.Request) error {
	writeJSON(w, http.StatusOK, statusResponse{Status: statusOK})
	return nil
}

func (h *Handler) readiness(w http.ResponseWriter, r *http.Request) error {
	ctx, cancel := context.WithTimeout(r.Context(), h.readyPingTimeout)
	defer cancel()

	if err := h.ready(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, statusResponse{Status: statusNotReady})
		return nil
	}
	writeJSON(w, http.StatusOK, statusResponse{Status: statusReady})
	return nil
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) error {
	var request registerRequest
	if err := h.decodeJSON(w, r, &request); err != nil {
		return err
	}

	user, err := h.auth.Register(r.Context(), request.ClubID, request.Email, request.Password)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, newUserResponse(user))
	return nil
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) error {
	var request loginRequest
	if err := h.decodeJSON(w, r, &request); err != nil {
		return err
	}

	session, err := h.auth.Login(r.Context(), request.Email, request.Password)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, newSessionResponse(session))
	return nil
}

func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) error {
	var request refreshRequest
	if err := h.decodeJSON(w, r, &request); err != nil {
		return err
	}

	session, err := h.auth.Refresh(r.Context(), request.RefreshToken)
	if err != nil {
		var reuse *domain.RefreshTokenReuseError
		if errors.As(err, &reuse) {
			h.logger.Warn("refresh token reuse detected",
				"event", EventRefreshTokenReuse,
				"user_id", reuse.Principal.UserID,
				"club_id", reuse.Principal.ClubID,
				"family_id", reuse.FamilyID,
			)
		}
		return err
	}
	writeJSON(w, http.StatusOK, newSessionResponse(session))
	return nil
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) error {
	principal, err := principalFrom(r)
	if err != nil {
		return err
	}
	var request refreshRequest
	if err := h.decodeJSON(w, r, &request); err != nil {
		return err
	}
	if request.RefreshToken == "" {
		return &domain.ValidationError{Fields: map[string]string{"refresh_token": "must not be empty"}}
	}

	if err := h.auth.Logout(r.Context(), principal, request.RefreshToken); err != nil {
		return err
	}
	writeNoContent(w)
	return nil
}

func (h *Handler) createClass(w http.ResponseWriter, r *http.Request) error {
	principal, err := principalFrom(r)
	if err != nil {
		return err
	}
	var request createClassRequest
	if err := h.decodeJSON(w, r, &request); err != nil {
		return err
	}

	class, err := h.bookings.CreateClass(r.Context(), principal, request.Title, request.StartsAt, request.Capacity)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, newClassResponse(class))
	return nil
}

func (h *Handler) listClasses(w http.ResponseWriter, r *http.Request) error {
	principal, err := principalFrom(r)
	if err != nil {
		return err
	}

	classes, err := h.bookings.ListClasses(r.Context(), principal)
	if err != nil {
		return err
	}

	response := listClassesResponse{Classes: make([]classResponse, 0, len(classes))}
	for _, class := range classes {
		response.Classes = append(response.Classes, newClassResponse(class))
	}
	writeJSON(w, http.StatusOK, response)
	return nil
}

// bookClass is the primary end-to-end walkthrough route.
func (h *Handler) bookClass(w http.ResponseWriter, r *http.Request) error {
	principal, err := principalFrom(r)
	if err != nil {
		return err
	}

	idempotencyKey := r.Header.Get(HeaderIdempotencyKey)
	if idempotencyKey != "" && !validHeaderValue(idempotencyKey, MaxIdempotencyKeyLen) {
		return &domain.ValidationError{Fields: map[string]string{
			"idempotency_key": fmt.Sprintf("must be at most %d printable ASCII characters", MaxIdempotencyKeyLen),
		}}
	}

	booking, err := h.bookings.BookClass(r.Context(), principal, r.PathValue("id"), idempotencyKey)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, newBookingResponse(booking))
	return nil
}

func (h *Handler) cancelBooking(w http.ResponseWriter, r *http.Request) error {
	principal, err := principalFrom(r)
	if err != nil {
		return err
	}

	if err := h.bookings.CancelBooking(r.Context(), principal, r.PathValue("id")); err != nil {
		return err
	}
	writeNoContent(w)
	return nil
}

func principalFrom(r *http.Request) (domain.Principal, error) {
	principal, ok := auth.PrincipalFromContext(r.Context())
	if !ok {
		return domain.Principal{}, domain.ErrUnauthorized
	}
	return principal, nil
}

// decodeJSON accepts exactly one JSON object, rejects unknown fields, and caps
// the body size with the configured limit.
func (h *Handler) decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, h.maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return err
		}
		return &domain.ValidationError{Fields: map[string]string{"body": "must be a valid JSON object"}}
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return &domain.ValidationError{Fields: map[string]string{"body": "must contain exactly one JSON object"}}
	}
	return nil
}

// Request and response DTOs are explicit so domain types are never serialized.
type registerRequest struct {
	ClubID   string `json:"club_id"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

type userResponse struct {
	ID        string      `json:"id"`
	ClubID    string      `json:"club_id"`
	Email     string      `json:"email"`
	Role      domain.Role `json:"role"`
	CreatedAt time.Time   `json:"created_at"`
}

func newUserResponse(user domain.User) userResponse {
	return userResponse{
		ID:        user.ID,
		ClubID:    user.ClubID,
		Email:     user.Email,
		Role:      user.Role,
		CreatedAt: user.CreatedAt,
	}
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type sessionResponse struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	TokenType    string    `json:"token_type"`
	ExpiresAt    time.Time `json:"expires_at"`
}

func newSessionResponse(session domain.Session) sessionResponse {
	return sessionResponse{
		AccessToken:  session.AccessToken,
		RefreshToken: session.RefreshToken,
		TokenType:    auth.TokenTypeBearer,
		ExpiresAt:    session.ExpiresAt,
	}
}

type createClassRequest struct {
	Title    string    `json:"title"`
	StartsAt time.Time `json:"starts_at"`
	Capacity int       `json:"capacity"`
}

type classResponse struct {
	ID          string    `json:"id"`
	ClubID      string    `json:"club_id"`
	TrainerID   string    `json:"trainer_id"`
	Title       string    `json:"title"`
	StartsAt    time.Time `json:"starts_at"`
	Capacity    int       `json:"capacity"`
	BookedCount int       `json:"booked_count"`
	CreatedAt   time.Time `json:"created_at"`
}

func newClassResponse(class domain.Class) classResponse {
	return classResponse{
		ID:          class.ID,
		ClubID:      class.ClubID,
		TrainerID:   class.TrainerID,
		Title:       class.Title,
		StartsAt:    class.StartsAt,
		Capacity:    class.Capacity,
		BookedCount: class.BookedCount,
		CreatedAt:   class.CreatedAt,
	}
}

type listClassesResponse struct {
	Classes []classResponse `json:"classes"`
}

type bookingResponse struct {
	ID        string    `json:"id"`
	ClubID    string    `json:"club_id"`
	ClassID   string    `json:"class_id"`
	UserID    string    `json:"user_id"`
	CreatedAt time.Time `json:"created_at"`
}

func newBookingResponse(booking domain.Booking) bookingResponse {
	return bookingResponse{
		ID:        booking.ID,
		ClubID:    booking.ClubID,
		ClassID:   booking.ClassID,
		UserID:    booking.UserID,
		CreatedAt: booking.CreatedAt,
	}
}

type statusResponse struct {
	Status string `json:"status"`
}
