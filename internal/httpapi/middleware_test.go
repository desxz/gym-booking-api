package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/muratgun/gym-booking-api/internal/auth"
	"github.com/muratgun/gym-booking-api/internal/domain"
)

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("random source failed") }

func TestSecurityHeaders(t *testing.T) {
	handler := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	assert.Equal(t, contentTypeNoSniff, recorder.Header().Get(headerContentTypeOptions))
	assert.Equal(t, frameOptionsDeny, recorder.Header().Get(headerFrameOptions))
	assert.Equal(t, cacheControlNoStore, recorder.Header().Get(headerCacheControl))
}

func TestRequestID(t *testing.T) {
	t.Run("accepts a safe client id", func(t *testing.T) {
		var fromContext string
		handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fromContext = RequestIDFromContext(r.Context())
			w.WriteHeader(http.StatusNoContent)
		}))
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.Header.Set(HeaderRequestID, "client-request-id")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)

		assert.Equal(t, "client-request-id", recorder.Header().Get(HeaderRequestID))
		assert.Equal(t, "client-request-id", fromContext)
	})

	t.Run("generates an id when the client id is missing or invalid", func(t *testing.T) {
		for _, header := range []string{"", strings.Repeat("a", MaxRequestIDLen+1)} {
			var fromContext string
			handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fromContext = RequestIDFromContext(r.Context())
			}))
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			if header != "" {
				request.Header.Set(HeaderRequestID, header)
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)

			id := recorder.Header().Get(HeaderRequestID)
			assert.Len(t, id, 32)
			assert.Equal(t, id, fromContext)
		}
	})

	t.Run("falls back when crypto randomness fails", func(t *testing.T) {
		previous := requestIDRandom
		requestIDRandom = failingReader{}
		t.Cleanup(func() { requestIDRandom = previous })

		handler := RequestID(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
		assert.Equal(t, requestIDFallback, recorder.Header().Get(HeaderRequestID))
	})

	t.Run("request id missing from context", func(t *testing.T) {
		assert.Empty(t, RequestIDFromContext(context.Background()))
	})
}

func TestSanitizeRequestID(t *testing.T) {
	tests := []struct {
		value string
		want  string
	}{
		{"abc", "abc"},
		{strings.Repeat("a", MaxRequestIDLen), strings.Repeat("a", MaxRequestIDLen)},
		{strings.Repeat("a", MaxRequestIDLen+1), ""},
		{"", ""},
		{"bad\x1fvalue", ""},
		{"line\nbreak", ""},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			assert.Equal(t, tt.want, sanitizeRequestID(tt.value))
		})
	}
}

func TestAccessLog(t *testing.T) {
	t.Run("logs status and authenticated identity", func(t *testing.T) {
		var logs bytes.Buffer
		logger := testLogger(&logs)
		handler := chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			info := r.Context().Value(requestInfoKey{}).(*requestInfo)
			info.userID, info.clubID = testUser, testClub
			w.WriteHeader(http.StatusCreated)
		}), RequestID, AccessLog(logger))

		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/classes", nil))

		var entry map[string]any
		require.NoError(t, json.NewDecoder(&logs).Decode(&entry))
		assert.Equal(t, float64(http.StatusCreated), entry["status"])
		assert.Equal(t, http.MethodPost, entry["method"])
		assert.Equal(t, "/api/v1/classes", entry["path"])
		assert.Equal(t, testUser, entry["user_id"])
		assert.Equal(t, testClub, entry["club_id"])
		assert.NotEmpty(t, entry["request_id"])
	})

	t.Run("omits identity when the request is anonymous", func(t *testing.T) {
		var logs bytes.Buffer
		handler := AccessLog(testLogger(&logs))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}))
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
		assert.NotContains(t, logs.String(), "user_id")
	})
}

func TestRecover(t *testing.T) {
	t.Run("recovers a panic into an opaque 500", func(t *testing.T) {
		var logs bytes.Buffer
		handler := chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic("boom")
		}), RequestID, Recover(testLogger(&logs)))

		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/panic", nil))

		require.Equal(t, http.StatusInternalServerError, recorder.Code)
		body := decodeErrorBody(t, recorder)
		assert.Equal(t, domain.CodeInternal, body.Code)
		assert.NotEmpty(t, body.RequestID)
		assert.Equal(t, recorder.Header().Get(HeaderRequestID), body.RequestID)
		assert.NotContains(t, recorder.Body.String(), "boom")
		assert.NotContains(t, recorder.Body.String(), "goroutine")

		logged := logs.String()
		assert.Contains(t, logged, "panic recovered")
		assert.Contains(t, logged, "stack")
	})

	t.Run("passes normal responses through", func(t *testing.T) {
		handler := Recover(discardLogger())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}))
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
		assert.Equal(t, http.StatusNoContent, recorder.Code)
	})
}

func TestAccessLogRecordsRecoveredPanic(t *testing.T) {
	var logs bytes.Buffer
	logger := testLogger(&logs)
	handler := chain(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }),
		RequestID,
		AccessLog(logger),
		Recover(logger),
	)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/panic", nil))

	require.Equal(t, http.StatusInternalServerError, recorder.Code)
	body := recorder.Body.String()
	assert.NotContains(t, body, "boom")
	assert.NotContains(t, body, "goroutine")
	assert.NotContains(t, body, "panic")

	require.NotEmpty(t, logs.String())
	var accessEntry map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var entry map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &entry))
		if entry["msg"] == "request completed" {
			accessEntry = entry
		}
	}
	require.NotNil(t, accessEntry)
	assert.Equal(t, float64(http.StatusInternalServerError), accessEntry["status"])
}

func TestRequestTimeout(t *testing.T) {
	var sawDeadline bool
	handler := RequestTimeout(0)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		sawDeadline = errors.Is(r.Context().Err(), context.DeadlineExceeded)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/slow", nil))
	assert.True(t, sawDeadline, "handler did not observe the request deadline")
}

func TestRequireAuth(t *testing.T) {
	tests := []struct {
		name       string
		header     string
		verifyErr  error
		principal  domain.Principal
		wantStatus int
	}{
		{"missing header", "", nil, domain.Principal{}, http.StatusUnauthorized},
		{"wrong scheme", "Basic valid", nil, domain.Principal{}, http.StatusUnauthorized},
		{"empty bearer token", "Bearer ", nil, domain.Principal{}, http.StatusUnauthorized},
		{"verifier failure", "Bearer valid", domain.ErrUnauthorized, domain.Principal{}, http.StatusUnauthorized},
		{"success", "Bearer valid", nil, testPrincipal, http.StatusNoContent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			verifier := NewMockTokenVerifier(ctrl)
			if tt.wantStatus != http.StatusUnauthorized || tt.verifyErr != nil {
				verifier.EXPECT().Verify(testToken).Return(tt.principal, tt.verifyErr).Times(1)
			}

			handler := RequireAuth(verifier, discardLogger())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				principal, ok := auth.PrincipalFromContext(r.Context())
				require.True(t, ok)
				assert.Equal(t, tt.principal, principal)
				w.WriteHeader(http.StatusNoContent)
			}))
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.header != "" {
				request.Header.Set(HeaderAuthorization, tt.header)
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			assert.Equal(t, tt.wantStatus, recorder.Code)
		})
	}

	t.Run("accepts lowercase bearer", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		verifier := NewMockTokenVerifier(ctrl)
		verifier.EXPECT().Verify(testToken).Return(testPrincipal, nil).Times(1)

		handler := RequireAuth(verifier, discardLogger())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}))
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.Header.Set(HeaderAuthorization, "bearer valid")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		assert.Equal(t, http.StatusNoContent, recorder.Code)
	})
}

func TestRequireRole(t *testing.T) {
	tests := []struct {
		name       string
		principal  *domain.Principal
		role       domain.Role
		wantStatus int
	}{
		{"missing principal", nil, domain.RoleTrainer, http.StatusUnauthorized},
		{"wrong role", &testPrincipal, domain.RoleTrainer, http.StatusForbidden},
		{"matching role", &testPrincipal, domain.RoleMember, http.StatusNoContent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/", nil)
			if tt.principal != nil {
				request = request.WithContext(auth.WithPrincipal(request.Context(), *tt.principal))
			}
			handler := RequireRole(tt.role, discardLogger())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			}))
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			assert.Equal(t, tt.wantStatus, recorder.Code)
		})
	}
}

func TestBearerToken(t *testing.T) {
	tests := []struct {
		header    string
		wantToken string
		wantOK    bool
	}{
		{"Bearer token", "token", true},
		{"bearer token", "token", true},
		{"BEARER token", "token", true},
		{"Bearer   spaced  ", "spaced", true},
		{"Bearer ", "", false},
		{"Bearer", "", false},
		{"", "", false},
		{"Basic token", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.header, func(t *testing.T) {
			token, ok := bearerToken(tt.header)
			assert.Equal(t, tt.wantToken, token)
			assert.Equal(t, tt.wantOK, ok)
		})
	}
}

func TestValidHeaderValue(t *testing.T) {
	tests := []struct {
		value  string
		maxLen int
		want   bool
	}{
		{"abc", 3, true},
		{"abcd", 3, false},
		{"", 3, false},
		{"a\x00b", 3, false},
		{"a\x7fb", 3, false},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			assert.Equal(t, tt.want, validHeaderValue(tt.value, tt.maxLen))
		})
	}
}
