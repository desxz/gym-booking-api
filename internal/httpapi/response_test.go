package httpapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/muratgun/gym-booking-api/internal/domain"
)

func TestWriteJSON(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeJSON(recorder, http.StatusCreated, statusResponse{Status: statusOK})

	assert.Equal(t, http.StatusCreated, recorder.Code)
	assert.Equal(t, ContentTypeJSON, recorder.Header().Get(HeaderContentType))
	assert.Equal(t, statusOK, decodeBody[statusResponse](t, recorder).Status)
}

func TestWriteNoContent(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeNoContent(recorder)
	assert.Equal(t, http.StatusNoContent, recorder.Code)
	assert.Zero(t, recorder.Body.Len())
}

func TestWriteErrorCanceled(t *testing.T) {
	var logs bytes.Buffer
	request := httptest.NewRequest(http.MethodGet, "/api/v1/classes", nil)
	request = request.WithContext(context.WithValue(request.Context(), requestIDKey{}, "rid-canceled"))
	recorder := httptest.NewRecorder()

	writeError(recorder, request, testLogger(&logs), context.Canceled)

	assert.Zero(t, recorder.Body.Len())
	logged := logs.String()
	assert.Contains(t, logged, `"level":"WARN"`)
	assert.NotContains(t, logged, `"level":"ERROR"`)
	assert.Contains(t, logged, `"request_id":"rid-canceled"`)
	assert.Contains(t, logged, `"method":"GET"`)
	assert.Contains(t, logged, `"/api/v1/classes"`)
}

func TestWriteErrorMappings(t *testing.T) {
	reuse := &domain.RefreshTokenReuseError{Principal: testPrincipal, FamilyID: "family"}
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
		wantFields bool
	}{
		{"validation", &domain.ValidationError{Fields: map[string]string{"title": "must not be empty"}}, http.StatusBadRequest, domain.CodeValidationFailed, true},
		{"body too large", &http.MaxBytesError{Limit: 1}, http.StatusRequestEntityTooLarge, domain.CodePayloadTooLarge, false},
		{"deadline", context.DeadlineExceeded, http.StatusGatewayTimeout, domain.CodeTimeout, false},
		{"invalid credentials", domain.ErrInvalidCredentials, http.StatusUnauthorized, domain.CodeInvalidCredentials, false},
		{"unauthorized", domain.ErrUnauthorized, http.StatusUnauthorized, domain.CodeUnauthorized, false},
		{"refresh reuse", reuse, http.StatusUnauthorized, domain.CodeUnauthorized, false},
		{"forbidden", domain.ErrForbidden, http.StatusForbidden, domain.CodeForbidden, false},
		{"not found", domain.ErrNotFound, http.StatusNotFound, domain.CodeNotFound, false},
		{"class full", domain.ErrClassFull, http.StatusConflict, domain.CodeClassFull, false},
		{"already booked", domain.ErrAlreadyBooked, http.StatusConflict, domain.CodeAlreadyBooked, false},
		{"class started", domain.ErrClassAlreadyStarted, http.StatusConflict, domain.CodeClassStarted, false},
		{"email taken", domain.ErrEmailTaken, http.StatusConflict, domain.CodeEmailTaken, false},
		{"idempotency conflict", domain.ErrIdempotencyConflict, http.StatusConflict, domain.CodeIdempotencyConflict, false},
		{"wrapped sentinel", fmt.Errorf("wrapped: %w", domain.ErrClassFull), http.StatusConflict, domain.CodeClassFull, false},
		{"unknown", errors.New("SELECT password_hash FROM users"), http.StatusInternalServerError, domain.CodeInternal, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request = request.WithContext(context.WithValue(request.Context(), requestIDKey{}, "rid-1"))

			recorder := httptest.NewRecorder()
			writeError(recorder, request, testLogger(&logs), tt.err)

			require.Equal(t, tt.wantStatus, recorder.Code)
			body := decodeErrorBody(t, recorder)
			assert.Equal(t, tt.wantCode, body.Code)
			assert.Equal(t, "rid-1", body.RequestID)
			assert.NotEmpty(t, body.Message)
			if tt.wantFields {
				assert.NotEmpty(t, body.Fields)
			} else {
				assert.Nil(t, body.Fields)
			}
			assert.NotZero(t, logs.Len())
		})
	}

	t.Run("unknown errors never leak internals", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		writeError(recorder, request, discardLogger(), errors.New("SELECT * FROM refresh_tokens WHERE token_hash='abc'"))
		raw := recorder.Body.String()
		for _, leak := range []string{"SELECT", "refresh_tokens", "token_hash"} {
			assert.NotContains(t, raw, leak)
		}
		assert.Equal(t, messageInternal, decodeErrorBody(t, recorder).Message)
	})

	t.Run("5xx logs at error level", func(t *testing.T) {
		var logs bytes.Buffer
		recorder := httptest.NewRecorder()
		writeError(recorder, httptest.NewRequest(http.MethodGet, "/", nil), testLogger(&logs), errors.New("boom"))
		assert.Contains(t, logs.String(), `"level":"ERROR"`)
	})
}
