package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/muratgun/gym-booking-api/internal/domain"
)

func TestHealthAndReadiness(t *testing.T) {
	api := newTestAPI(t)

	recorder := serve(api.handler, http.MethodGet, "/healthz", "", "")
	require.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, statusOK, decodeBody[statusResponse](t, recorder).Status)

	recorder = serve(api.handler, http.MethodGet, "/readyz", "", "")
	require.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, statusReady, decodeBody[statusResponse](t, recorder).Status)

	api.ready = func(context.Context) error { return errors.New("database down") }
	recorder = serve(api.handler, http.MethodGet, "/readyz", "", "")
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	assert.Equal(t, statusNotReady, decodeBody[statusResponse](t, recorder).Status)
}

func TestRegister(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		api := newTestAPI(t)
		api.auth.EXPECT().
			Register(gomock.Any(), testClub, "new@example.com", "Password123!").
			Return(domain.User{
				ID: testUser, ClubID: testClub, Email: "new@example.com",
				Role: domain.RoleMember, CreatedAt: testNow,
			}, nil).Times(1)

		body := `{"club_id":"` + testClub + `","email":"new@example.com","password":"Password123!"}`
		recorder := serve(api.handler, http.MethodPost, "/api/v1/auth/register", body, "")
		require.Equal(t, http.StatusCreated, recorder.Code)

		response := decodeBody[userResponse](t, recorder)
		assert.Equal(t, testUser, response.ID)
		assert.Equal(t, "new@example.com", response.Email)
		assert.Equal(t, domain.RoleMember, response.Role)
	})

	t.Run("service error", func(t *testing.T) {
		api := newTestAPI(t)
		api.auth.EXPECT().
			Register(gomock.Any(), testClub, "member@club-a.test", "Password123!").
			Return(domain.User{}, domain.ErrEmailTaken).Times(1)

		body := `{"club_id":"` + testClub + `","email":"member@club-a.test","password":"Password123!"}`
		recorder := serve(api.handler, http.MethodPost, "/api/v1/auth/register", body, "")
		require.Equal(t, http.StatusConflict, recorder.Code)
		assert.Equal(t, domain.CodeEmailTaken, decodeErrorBody(t, recorder).Code)
	})

	t.Run("malformed JSON", func(t *testing.T) {
		api := newTestAPI(t)
		recorder := serve(api.handler, http.MethodPost, "/api/v1/auth/register", "{", "")
		require.Equal(t, http.StatusBadRequest, recorder.Code)
		assert.Contains(t, decodeErrorBody(t, recorder).Fields, "body")
	})

	t.Run("unknown field rejected", func(t *testing.T) {
		api := newTestAPI(t)
		body := `{"club_id":"` + testClub + `","email":"new@example.com","password":"Password123!","role":"trainer"}`
		recorder := serve(api.handler, http.MethodPost, "/api/v1/auth/register", body, "")
		require.Equal(t, http.StatusBadRequest, recorder.Code)
	})
}

func TestLogin(t *testing.T) {
	t.Run("success returns both tokens", func(t *testing.T) {
		api := newTestAPI(t)
		api.auth.EXPECT().
			Login(gomock.Any(), "member@club-a.test", "Password123!").
			Return(testSession, nil).Times(1)

		body := `{"email":"member@club-a.test","password":"Password123!"}`
		recorder := serve(api.handler, http.MethodPost, "/api/v1/auth/login", body, "")
		require.Equal(t, http.StatusOK, recorder.Code)

		response := decodeBody[sessionResponse](t, recorder)
		assert.Equal(t, testSession.AccessToken, response.AccessToken)
		assert.Equal(t, testSession.RefreshToken, response.RefreshToken)
		assert.Equal(t, "Bearer", response.TokenType)
		assert.Equal(t, testSession.ExpiresAt, response.ExpiresAt)
	})

	t.Run("invalid credentials", func(t *testing.T) {
		api := newTestAPI(t)
		api.auth.EXPECT().
			Login(gomock.Any(), "member@club-a.test", "wrong").
			Return(domain.Session{}, domain.ErrInvalidCredentials).Times(1)

		body := `{"email":"member@club-a.test","password":"wrong"}`
		recorder := serve(api.handler, http.MethodPost, "/api/v1/auth/login", body, "")
		require.Equal(t, http.StatusUnauthorized, recorder.Code)
		assert.Equal(t, domain.CodeInvalidCredentials, decodeErrorBody(t, recorder).Code)
	})

	t.Run("malformed JSON", func(t *testing.T) {
		api := newTestAPI(t)
		recorder := serve(api.handler, http.MethodPost, "/api/v1/auth/login", `{"email":`, "")
		require.Equal(t, http.StatusBadRequest, recorder.Code)
	})
}

func TestRefresh(t *testing.T) {
	t.Run("success rotates tokens", func(t *testing.T) {
		api := newTestAPI(t)
		api.auth.EXPECT().Refresh(gomock.Any(), "raw-refresh").Return(testSession, nil).Times(1)

		recorder := serve(api.handler, http.MethodPost, "/api/v1/auth/refresh", `{"refresh_token":"raw-refresh"}`, "")
		require.Equal(t, http.StatusOK, recorder.Code)

		response := decodeBody[sessionResponse](t, recorder)
		assert.Equal(t, testSession.AccessToken, response.AccessToken)
		assert.Equal(t, testSession.RefreshToken, response.RefreshToken)
	})

	t.Run("reuse is logged without the token", func(t *testing.T) {
		api := newTestAPI(t)
		api.auth.EXPECT().Refresh(gomock.Any(), "stolen-raw-token").
			Return(domain.Session{}, &domain.RefreshTokenReuseError{
				Principal: testPrincipal,
				FamilyID:  "family-1",
			}).Times(1)

		recorder := serve(api.handler, http.MethodPost, "/api/v1/auth/refresh", `{"refresh_token":"stolen-raw-token"}`, "")
		require.Equal(t, http.StatusUnauthorized, recorder.Code)

		logged := api.logs.String()
		assert.Contains(t, logged, EventRefreshTokenReuse)
		assert.Contains(t, logged, testUser)
		assert.Contains(t, logged, testClub)
		assert.Contains(t, logged, "family-1")
		assert.NotContains(t, logged, "stolen-raw-token")
	})

	t.Run("generic service error is opaque", func(t *testing.T) {
		api := newTestAPI(t)
		api.auth.EXPECT().Refresh(gomock.Any(), "raw").
			Return(domain.Session{}, errors.New("SELECT * FROM refresh_tokens")).Times(1)

		recorder := serve(api.handler, http.MethodPost, "/api/v1/auth/refresh", `{"refresh_token":"raw"}`, "")
		require.Equal(t, http.StatusInternalServerError, recorder.Code)
		assert.NotContains(t, recorder.Body.String(), "SELECT")
	})

	t.Run("missing token is unauthorized", func(t *testing.T) {
		api := newTestAPI(t)
		api.auth.EXPECT().Refresh(gomock.Any(), "").Return(domain.Session{}, domain.ErrUnauthorized).Times(1)

		recorder := serve(api.handler, http.MethodPost, "/api/v1/auth/refresh", `{}`, "")
		require.Equal(t, http.StatusUnauthorized, recorder.Code)
	})

	t.Run("malformed JSON", func(t *testing.T) {
		api := newTestAPI(t)
		recorder := serve(api.handler, http.MethodPost, "/api/v1/auth/refresh", "{", "")
		require.Equal(t, http.StatusBadRequest, recorder.Code)
	})
}

func TestLogout(t *testing.T) {
	t.Run("revokes with the authenticated actor", func(t *testing.T) {
		api := newTestAPI(t)
		expectValidToken(api, testPrincipal)
		api.auth.EXPECT().
			Logout(gomock.Any(), gomock.Eq(testPrincipal), "raw-refresh").
			Return(nil).Times(1)

		recorder := serve(api.handler, http.MethodPost, "/api/v1/auth/logout", `{"refresh_token":"raw-refresh"}`, testToken)
		require.Equal(t, http.StatusNoContent, recorder.Code)
	})

	t.Run("missing refresh token is a validation error", func(t *testing.T) {
		api := newTestAPI(t)
		expectValidToken(api, testPrincipal)

		recorder := serve(api.handler, http.MethodPost, "/api/v1/auth/logout", `{}`, testToken)
		require.Equal(t, http.StatusBadRequest, recorder.Code)
	})

	t.Run("service error", func(t *testing.T) {
		api := newTestAPI(t)
		expectValidToken(api, testPrincipal)
		api.auth.EXPECT().Logout(gomock.Any(), gomock.Eq(testPrincipal), "raw").
			Return(errors.New("revoke failed")).Times(1)

		recorder := serve(api.handler, http.MethodPost, "/api/v1/auth/logout", `{"refresh_token":"raw"}`, testToken)
		require.Equal(t, http.StatusInternalServerError, recorder.Code)
	})

	t.Run("malformed JSON", func(t *testing.T) {
		api := newTestAPI(t)
		expectValidToken(api, testPrincipal)

		recorder := serve(api.handler, http.MethodPost, "/api/v1/auth/logout", "{", testToken)
		require.Equal(t, http.StatusBadRequest, recorder.Code)
	})

	t.Run("requires authentication", func(t *testing.T) {
		api := newTestAPI(t)
		recorder := serve(api.handler, http.MethodPost, "/api/v1/auth/logout", `{"refresh_token":"raw"}`, "")
		require.Equal(t, http.StatusUnauthorized, recorder.Code)
	})
}

func TestCreateClass(t *testing.T) {
	trainer := domain.Principal{UserID: testTrainerID, ClubID: testClub, Role: domain.RoleTrainer}

	t.Run("trainer creates a class", func(t *testing.T) {
		api := newTestAPI(t)
		expectValidToken(api, trainer)
		api.bookings.EXPECT().
			CreateClass(gomock.Any(), gomock.Eq(trainer), "Spin", testNow, 12).
			Return(domain.Class{
				ID: testClassID, ClubID: testClub, TrainerID: testTrainerID, Title: "Spin", Capacity: 12,
			}, nil).Times(1)

		body := `{"title":"Spin","starts_at":"` + testNow.Format(time.RFC3339) + `","capacity":12}`
		recorder := serve(api.handler, http.MethodPost, "/api/v1/classes", body, testToken)
		require.Equal(t, http.StatusCreated, recorder.Code)
		assert.Equal(t, testClassID, decodeBody[classResponse](t, recorder).ID)
	})

	t.Run("member is forbidden", func(t *testing.T) {
		api := newTestAPI(t)
		expectValidToken(api, testPrincipal)

		body := `{"title":"Spin","starts_at":"` + testNow.Format(time.RFC3339) + `","capacity":12}`
		recorder := serve(api.handler, http.MethodPost, "/api/v1/classes", body, testToken)
		require.Equal(t, http.StatusForbidden, recorder.Code)
	})

	t.Run("service error", func(t *testing.T) {
		api := newTestAPI(t)
		expectValidToken(api, trainer)
		api.bookings.EXPECT().CreateClass(gomock.Any(), gomock.Eq(trainer), "", testNow, 12).
			Return(domain.Class{}, &domain.ValidationError{
				Fields: map[string]string{"title": "must not be empty"},
			}).Times(1)

		body := `{"title":"","starts_at":"` + testNow.Format(time.RFC3339) + `","capacity":12}`
		recorder := serve(api.handler, http.MethodPost, "/api/v1/classes", body, testToken)
		require.Equal(t, http.StatusBadRequest, recorder.Code)
	})

	t.Run("malformed JSON", func(t *testing.T) {
		api := newTestAPI(t)
		expectValidToken(api, trainer)

		recorder := serve(api.handler, http.MethodPost, "/api/v1/classes", "{", testToken)
		require.Equal(t, http.StatusBadRequest, recorder.Code)
	})
}

func TestListClasses(t *testing.T) {
	t.Run("returns classes for the actor's club", func(t *testing.T) {
		api := newTestAPI(t)
		expectValidToken(api, testPrincipal)
		api.bookings.EXPECT().ListClasses(gomock.Any(), gomock.Eq(testPrincipal)).
			Return([]domain.Class{
				{ID: testClassID, ClubID: testClub, Title: "Spin"},
				{ID: "33333333-3333-3333-3333-333333333302", ClubID: testClub, Title: "HIIT"},
			}, nil).Times(1)

		recorder := serve(api.handler, http.MethodGet, "/api/v1/classes", "", testToken)
		require.Equal(t, http.StatusOK, recorder.Code)

		response := decodeBody[listClassesResponse](t, recorder)
		require.Len(t, response.Classes, 2)
		assert.Equal(t, testClassID, response.Classes[0].ID)
	})

	t.Run("empty list is an empty array", func(t *testing.T) {
		api := newTestAPI(t)
		expectValidToken(api, testPrincipal)
		api.bookings.EXPECT().ListClasses(gomock.Any(), gomock.Eq(testPrincipal)).
			Return(nil, nil).Times(1)

		recorder := serve(api.handler, http.MethodGet, "/api/v1/classes", "", testToken)
		require.Equal(t, http.StatusOK, recorder.Code)
		assert.NotContains(t, recorder.Body.String(), "null")
	})

	t.Run("service error", func(t *testing.T) {
		api := newTestAPI(t)
		expectValidToken(api, testPrincipal)
		api.bookings.EXPECT().ListClasses(gomock.Any(), gomock.Eq(testPrincipal)).
			Return(nil, errors.New("query failed")).Times(1)

		recorder := serve(api.handler, http.MethodGet, "/api/v1/classes", "", testToken)
		require.Equal(t, http.StatusInternalServerError, recorder.Code)
	})
}

func TestBookClass(t *testing.T) {
	book := func(api *testAPI, key string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/classes/"+testClassID+"/bookings", nil)
		request.Header.Set(HeaderAuthorization, "Bearer "+testToken)
		if key != "" {
			request.Header.Set(HeaderIdempotencyKey, key)
		}
		recorder := httptest.NewRecorder()
		api.handler.ServeHTTP(recorder, request)
		return recorder
	}

	t.Run("success forwards the idempotency key", func(t *testing.T) {
		api := newTestAPI(t)
		expectValidToken(api, testPrincipal)
		api.bookings.EXPECT().
			BookClass(gomock.Any(), gomock.Eq(testPrincipal), testClassID, "idem-1").
			Return(domain.Booking{ID: testBookingID, ClubID: testClub, ClassID: testClassID, UserID: testUser}, nil).Times(1)

		recorder := book(api, "idem-1")
		require.Equal(t, http.StatusCreated, recorder.Code)
		assert.Equal(t, testBookingID, decodeBody[bookingResponse](t, recorder).ID)
	})

	t.Run("success without a key", func(t *testing.T) {
		api := newTestAPI(t)
		expectValidToken(api, testPrincipal)
		api.bookings.EXPECT().
			BookClass(gomock.Any(), gomock.Eq(testPrincipal), testClassID, "").
			Return(domain.Booking{ID: testBookingID}, nil).Times(1)

		recorder := book(api, "")
		require.Equal(t, http.StatusCreated, recorder.Code)
	})

	t.Run("overlong idempotency key is rejected", func(t *testing.T) {
		api := newTestAPI(t)
		expectValidToken(api, testPrincipal)

		recorder := book(api, strings.Repeat("a", MaxIdempotencyKeyLen+1))
		require.Equal(t, http.StatusBadRequest, recorder.Code)
		assert.Contains(t, decodeErrorBody(t, recorder).Fields, "idempotency_key")
	})

	t.Run("non-printable idempotency key is rejected", func(t *testing.T) {
		api := newTestAPI(t)
		expectValidToken(api, testPrincipal)
		request := httptest.NewRequest(http.MethodPost, "/api/v1/classes/"+testClassID+"/bookings", nil)
		request.Header.Set(HeaderAuthorization, "bearer "+testToken)
		request.Header[HeaderIdempotencyKey] = []string{"bad\x01key"}
		recorder := httptest.NewRecorder()
		api.handler.ServeHTTP(recorder, request)
		require.Equal(t, http.StatusBadRequest, recorder.Code)
	})

	t.Run("service conflict", func(t *testing.T) {
		api := newTestAPI(t)
		expectValidToken(api, testPrincipal)
		api.bookings.EXPECT().BookClass(gomock.Any(), gomock.Eq(testPrincipal), testClassID, "").
			Return(domain.Booking{}, domain.ErrAlreadyBooked).Times(1)

		recorder := book(api, "")
		require.Equal(t, http.StatusConflict, recorder.Code)
	})

	t.Run("trainer cannot book", func(t *testing.T) {
		api := newTestAPI(t)
		expectValidToken(api, domain.Principal{UserID: testTrainerID, ClubID: testClub, Role: domain.RoleTrainer})

		recorder := book(api, "")
		require.Equal(t, http.StatusForbidden, recorder.Code)
	})
}

func TestCancelBooking(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		api := newTestAPI(t)
		expectValidToken(api, testPrincipal)
		api.bookings.EXPECT().CancelBooking(gomock.Any(), gomock.Eq(testPrincipal), testBookingID).
			Return(nil).Times(1)

		recorder := serve(api.handler, http.MethodDelete, "/api/v1/bookings/"+testBookingID, "", testToken)
		require.Equal(t, http.StatusNoContent, recorder.Code)
	})

	t.Run("service error", func(t *testing.T) {
		api := newTestAPI(t)
		expectValidToken(api, testPrincipal)
		api.bookings.EXPECT().CancelBooking(gomock.Any(), gomock.Eq(testPrincipal), testBookingID).
			Return(domain.ErrNotFound).Times(1)

		recorder := serve(api.handler, http.MethodDelete, "/api/v1/bookings/"+testBookingID, "", testToken)
		require.Equal(t, http.StatusNotFound, recorder.Code)
	})
}

func TestHandlersRequirePrincipal(t *testing.T) {
	handler := newDirectHandler(nil, nil)
	tests := []struct {
		name string
		call func(http.ResponseWriter, *http.Request) error
	}{
		{"logout", handler.logout},
		{"create class", handler.createClass},
		{"list classes", handler.listClasses},
		{"book class", handler.bookClass},
		{"cancel booking", handler.cancelBooking},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
			require.ErrorIs(t, tt.call(recorder, request), domain.ErrUnauthorized)
		})
	}
}

func TestOversizedBody(t *testing.T) {
	api := newTestAPI(t)
	handler := NewRouter(RouterConfig{
		Auth:             api.auth,
		Bookings:         api.bookings,
		Tokens:           api.verifier,
		Logger:           discardLogger(),
		RequestTimeout:   time.Second,
		MaxBodyBytes:     8,
		ReadyPingTimeout: time.Second,
		ReadyCheck:       func(context.Context) error { return nil },
	})
	recorder := serve(handler, http.MethodPost, "/api/v1/auth/login", `{"email":"member@club-a.test"}`, "")
	require.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code)
	assert.Equal(t, domain.CodePayloadTooLarge, decodeErrorBody(t, recorder).Code)
}

func TestTrailingJSONAndEmptyBody(t *testing.T) {
	t.Run("trailing object", func(t *testing.T) {
		api := newTestAPI(t)
		body := `{"club_id":"` + testClub + `"} {"extra":true}`
		recorder := serve(api.handler, http.MethodPost, "/api/v1/auth/register", body, "")
		require.Equal(t, http.StatusBadRequest, recorder.Code)
		assert.Contains(t, decodeErrorBody(t, recorder).Fields, "body")
	})

	t.Run("empty body", func(t *testing.T) {
		api := newTestAPI(t)
		recorder := serve(api.handler, http.MethodPost, "/api/v1/auth/login", "", "")
		require.Equal(t, http.StatusBadRequest, recorder.Code)
	})
}
