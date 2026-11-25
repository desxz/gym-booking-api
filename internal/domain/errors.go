package domain

import (
	"errors"
	"fmt"
	"sort"
)

// Sentinel errors form the contract between layers. The HTTP layer maps them
// to status codes; repositories translate driver errors into them.
var (
	ErrNotFound            = errors.New("not found")
	ErrClassFull           = errors.New("class is full")
	ErrClassAlreadyStarted = errors.New("class has already started")
	ErrAlreadyBooked       = errors.New("already booked")
	ErrEmailTaken          = errors.New("email already registered")
	ErrInvalidCredentials  = errors.New("invalid credentials")
	ErrUnauthorized        = errors.New("unauthorized")
	ErrForbidden           = errors.New("forbidden")
	ErrIdempotencyConflict = errors.New("idempotency key reused with a different request")
)

// Stable public error codes. They live in the domain so every transport layer
// uses the same contract without inventing its own strings.
const (
	CodeValidationFailed    = "validation_failed"
	CodeInternal            = "internal_error"
	CodePayloadTooLarge     = "payload_too_large"
	CodeTimeout             = "timeout"
	CodeInvalidCredentials  = "invalid_credentials"
	CodeUnauthorized        = "unauthorized"
	CodeForbidden           = "forbidden"
	CodeNotFound            = "not_found"
	CodeClassFull           = "class_full"
	CodeAlreadyBooked       = "already_booked"
	CodeClassStarted        = "class_started"
	CodeEmailTaken          = "email_taken"
	CodeIdempotencyConflict = "idempotency_conflict"
)

// RefreshTokenReuseError reports a replay of an already-rotated refresh token.
// The repository has already revoked the whole family when this is returned.
type RefreshTokenReuseError struct {
	Principal Principal
	FamilyID  string
}

func (e *RefreshTokenReuseError) Error() string {
	return "refresh token reuse detected"
}

func (e *RefreshTokenReuseError) Unwrap() error {
	return ErrUnauthorized
}

// ValidationError reports all invalid input fields at once so clients can fix
// them in a single round trip.
type ValidationError struct {
	Fields map[string]string
}

func (e *ValidationError) Error() string {
	if len(e.Fields) == 0 {
		return "validation failed"
	}
	names := make([]string, 0, len(e.Fields))
	for name := range e.Fields {
		names = append(names, name)
	}
	sort.Strings(names)

	message := "validation failed"
	for i, name := range names {
		if i == 0 {
			message += ": "
		} else {
			message += "; "
		}
		message += fmt.Sprintf("%s %s", name, e.Fields[name])
	}
	return message
}
