package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/muratgun/gym-booking-api/internal/auth"
	"github.com/muratgun/gym-booking-api/internal/domain"
)

const (
	testToken     = "valid"
	testClub      = "11111111-1111-1111-1111-111111111111"
	testUser      = "11111111-1111-1111-1111-111111111102"
	testTrainerID = "11111111-1111-1111-1111-111111111101"
	testClassID   = "33333333-3333-3333-3333-333333333301"
	testBookingID = "44444444-4444-4444-4444-444444444401"
)

var (
	testNow       = time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	testPrincipal = domain.Principal{UserID: testUser, ClubID: testClub, Role: domain.RoleMember}
	testSession   = domain.Session{
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
		ExpiresAt:    testNow.Add(15 * time.Minute),
	}
)

type testAPI struct {
	handler  http.Handler
	auth     *MockAuthService
	bookings *MockBookingService
	verifier *MockTokenVerifier
	logs     *bytes.Buffer
	ready    func(context.Context) error
}

func newTestAPI(t *testing.T) *testAPI {
	t.Helper()
	ctrl := gomock.NewController(t)
	api := &testAPI{
		auth:     NewMockAuthService(ctrl),
		bookings: NewMockBookingService(ctrl),
		verifier: NewMockTokenVerifier(ctrl),
		logs:     &bytes.Buffer{},
	}
	api.ready = func(context.Context) error { return nil }
	api.handler = NewRouter(RouterConfig{
		Auth:             api.auth,
		Bookings:         api.bookings,
		Tokens:           api.verifier,
		Logger:           testLogger(api.logs),
		RequestTimeout:   time.Second,
		MaxBodyBytes:     1 << 20,
		ReadyPingTimeout: time.Second,
		ReadyCheck:       func(ctx context.Context) error { return api.ready(ctx) },
	})
	return api
}

func expectValidToken(api *testAPI, principal domain.Principal) {
	api.verifier.EXPECT().Verify(testToken).Return(principal, nil).Times(1)
}

func testLogger(writer io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(writer, nil))
}

func discardLogger() *slog.Logger {
	return testLogger(io.Discard)
}

func newDirectHandler(authService AuthService, bookings BookingService) *Handler {
	return &Handler{
		auth:             authService,
		bookings:         bookings,
		logger:           discardLogger(),
		ready:            func(context.Context) error { return nil },
		maxBodyBytes:     1 << 20,
		readyPingTimeout: time.Second,
	}
}

func serve(handler http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, path, reader)
	if body != "" {
		request.Header.Set(HeaderContentType, ContentTypeJSON)
	}
	if token != "" {
		request.Header.Set(HeaderAuthorization, auth.BearerPrefix+token)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func authenticatedRequest(method, path, body string, principal domain.Principal) *http.Request {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, path, reader)
	return request.WithContext(auth.WithPrincipal(request.Context(), principal))
}

type testErrorBody struct {
	Code      string            `json:"code"`
	Message   string            `json:"message"`
	RequestID string            `json:"request_id"`
	Fields    map[string]string `json:"fields"`
}

func decodeErrorBody(t *testing.T, recorder *httptest.ResponseRecorder) testErrorBody {
	t.Helper()
	var body testErrorBody
	require.NoError(t, json.NewDecoder(recorder.Body).Decode(&body), "decode error body")
	return body
}

func decodeBody[T any](t *testing.T, recorder *httptest.ResponseRecorder) T {
	t.Helper()
	var body T
	require.NoError(t, json.NewDecoder(recorder.Body).Decode(&body), "decode response body")
	return body
}
