package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/muratgun/gym-booking-api/internal/domain"
)

const (
	HeaderContentType = "Content-Type"
	ContentTypeJSON   = "application/json; charset=utf-8"

	messageInternal        = "an unexpected error occurred"
	messageValidation      = "request validation failed"
	messagePayloadTooLarge = "request body is too large"
	messageTimeout         = "request timed out"
)

// errorResponse is the single error shape for every non-2xx response.
type errorResponse struct {
	Code      string            `json:"code"`
	Message   string            `json:"message"`
	RequestID string            `json:"request_id"`
	Fields    map[string]string `json:"fields,omitempty"`
}

type errorMapping struct {
	target  error
	status  int
	code    string
	message string
}

var sentinelErrorMappings = []errorMapping{
	{domain.ErrInvalidCredentials, http.StatusUnauthorized, domain.CodeInvalidCredentials, "invalid email or password"},
	{domain.ErrUnauthorized, http.StatusUnauthorized, domain.CodeUnauthorized, "authentication required"},
	{domain.ErrForbidden, http.StatusForbidden, domain.CodeForbidden, "you do not have permission to perform this action"},
	{domain.ErrNotFound, http.StatusNotFound, domain.CodeNotFound, "resource not found"},
	{domain.ErrClassFull, http.StatusConflict, domain.CodeClassFull, "class is full"},
	{domain.ErrAlreadyBooked, http.StatusConflict, domain.CodeAlreadyBooked, "you already booked this class"},
	{domain.ErrClassAlreadyStarted, http.StatusConflict, domain.CodeClassStarted, "class has already started"},
	{domain.ErrEmailTaken, http.StatusConflict, domain.CodeEmailTaken, "email is already registered"},
	{domain.ErrIdempotencyConflict, http.StatusConflict, domain.CodeIdempotencyConflict, "idempotency key was reused with a different request"},
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set(HeaderContentType, ContentTypeJSON)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeNoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

// writeError is the only exit for failures. It maps domain errors to a stable
// public contract and logs 5xx details server-side only.
func writeError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	status := http.StatusInternalServerError
	code := domain.CodeInternal
	message := messageInternal
	var fields map[string]string

	var validation *domain.ValidationError
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &validation):
		status, code, message, fields = http.StatusBadRequest, domain.CodeValidationFailed, messageValidation, validation.Fields
	case errors.As(err, &tooLarge):
		status, code, message = http.StatusRequestEntityTooLarge, domain.CodePayloadTooLarge, messagePayloadTooLarge
	case errors.Is(err, context.DeadlineExceeded):
		status, code, message = http.StatusGatewayTimeout, domain.CodeTimeout, messageTimeout
	case errors.Is(err, context.Canceled):
		logger.Warn("request canceled",
			"request_id", RequestIDFromContext(r.Context()),
			"method", r.Method,
			"path", r.URL.Path,
			"error", err,
		)
		return
	default:
		for _, mapping := range sentinelErrorMappings {
			if errors.Is(err, mapping.target) {
				status, code, message = mapping.status, mapping.code, mapping.message
				break
			}
		}
	}

	requestID := RequestIDFromContext(r.Context())
	if status >= http.StatusInternalServerError {
		logger.Error("request failed", "request_id", requestID, "error", err)
	} else {
		logger.Warn("request rejected", "request_id", requestID, "status", status, "error", err)
	}

	writeJSON(w, status, errorResponse{
		Code:      code,
		Message:   message,
		RequestID: requestID,
		Fields:    fields,
	})
}
