package domain

import (
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var fixedNow = time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)

func TestRoleValid(t *testing.T) {
	tests := []struct {
		role Role
		want bool
	}{
		{RoleMember, true},
		{RoleTrainer, true},
		{Role("admin"), false},
		{Role(""), false},
	}
	for _, tt := range tests {
		t.Run(string(tt.role), func(t *testing.T) {
			assert.Equal(t, tt.want, tt.role.Valid())
		})
	}
}

func TestMaskEmail(t *testing.T) {
	tests := []struct {
		email string
		want  string
	}{
		{"member@club-a.test", "m***@club-a.test"},
		{"x@y", "x***@y"},
		{"no-at-sign", "***"},
		{"@domain", "***"},
		{"", "***"},
	}
	for _, tt := range tests {
		t.Run(tt.email, func(t *testing.T) {
			assert.Equal(t, tt.want, maskEmail(tt.email))
		})
	}
}

func TestUserLogValueMasksSensitiveData(t *testing.T) {
	user := User{
		ID:           "11111111-1111-1111-1111-111111111102",
		ClubID:       "11111111-1111-1111-1111-111111111111",
		Email:        "member@club-a.test",
		PasswordHash: "super-secret-hash",
		Role:         RoleMember,
	}

	value := user.LogValue()
	require.Equal(t, slog.KindGroup, value.Kind())

	attrs := map[string]string{}
	for _, attr := range value.Group() {
		attrs[attr.Key] = attr.Value.String()
	}
	assert.Equal(t, "m***@club-a.test", attrs["email"])
	for key, value := range attrs {
		assert.NotContains(t, value, "super-secret-hash", "attribute %q leaked the password hash", key)
	}
	assert.Equal(t, user.ID, attrs["id"])
	assert.Equal(t, user.ClubID, attrs["club_id"])
	assert.Equal(t, string(user.Role), attrs["role"])
}

func TestNewClass(t *testing.T) {
	t.Run("valid class is trimmed and formed", func(t *testing.T) {
		startsAt := fixedNow.Add(time.Hour)
		class, err := NewClass("club", "trainer", "  Morning Strength  ", startsAt, 12, fixedNow)
		require.NoError(t, err)
		assert.Equal(t, "Morning Strength", class.Title)
		assert.Equal(t, 12, class.Capacity)
		assert.Equal(t, startsAt, class.StartsAt)
		assert.Equal(t, "club", class.ClubID)
		assert.Equal(t, "trainer", class.TrainerID)
	})

	t.Run("unicode title boundary", func(t *testing.T) {
		title := strings.Repeat("é", MaxClassTitleLen)
		_, err := NewClass("club", "trainer", title, fixedNow.Add(time.Hour), 1, fixedNow)
		require.NoError(t, err)

		_, err = NewClass("club", "trainer", title+"é", fixedNow.Add(time.Hour), 1, fixedNow)
		require.Error(t, err)
	})

	tests := []struct {
		name      string
		title     string
		startsAt  time.Time
		capacity  int
		wantField string
	}{
		{"empty title", "", fixedNow.Add(time.Hour), 1, "title"},
		{"title too long", strings.Repeat("a", MaxClassTitleLen+1), fixedNow.Add(time.Hour), 1, "title"},
		{"capacity below minimum", "Spin", fixedNow.Add(time.Hour), MinClassCapacity - 1, "capacity"},
		{"capacity above maximum", "Spin", fixedNow.Add(time.Hour), MaxClassCapacity + 1, "capacity"},
		{"start in the past", "Spin", fixedNow.Add(-time.Minute), 1, "starts_at"},
		{"start exactly now", "Spin", fixedNow, 1, "starts_at"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewClass("club", "trainer", tt.title, tt.startsAt, tt.capacity, fixedNow)
			var validation *ValidationError
			require.ErrorAs(t, err, &validation)
			assert.Contains(t, validation.Fields, tt.wantField)
		})
	}

	t.Run("reports every invalid field at once", func(t *testing.T) {
		_, err := NewClass("club", "trainer", "", fixedNow, MinClassCapacity-1, fixedNow)
		var validation *ValidationError
		require.ErrorAs(t, err, &validation)
		for _, field := range []string{"title", "capacity", "starts_at"} {
			assert.Contains(t, validation.Fields, field)
		}
	})
}

func TestClassCanAcceptBooking(t *testing.T) {
	class := Class{StartsAt: fixedNow.Add(time.Hour), Capacity: 2, BookedCount: 1}
	require.NoError(t, class.CanAcceptBooking(fixedNow))
}

func TestClassCanAcceptBookingErrors(t *testing.T) {
	tests := []struct {
		name    string
		class   Class
		now     time.Time
		wantErr error
	}{
		{"started", Class{StartsAt: fixedNow, Capacity: 2, BookedCount: 0}, fixedNow, ErrClassAlreadyStarted},
		{"future and full", Class{StartsAt: fixedNow.Add(time.Hour), Capacity: 2, BookedCount: 2}, fixedNow, ErrClassFull},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.ErrorIs(t, tt.class.CanAcceptBooking(tt.now), tt.wantErr)
		})
	}
}

func TestBookingCanBeCancelledBy(t *testing.T) {
	booking := Booking{ClubID: "club-a", UserID: "member-a"}
	tests := []struct {
		name  string
		actor Principal
		want  bool
	}{
		{"owner", Principal{UserID: "member-a", ClubID: "club-a", Role: RoleMember}, true},
		{"other member", Principal{UserID: "member-b", ClubID: "club-a", Role: RoleMember}, false},
		{"trainer same club", Principal{UserID: "trainer", ClubID: "club-a", Role: RoleTrainer}, true},
		{"trainer other club", Principal{UserID: "trainer", ClubID: "club-b", Role: RoleTrainer}, false},
		{"owner other club", Principal{UserID: "member-a", ClubID: "club-b", Role: RoleMember}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, booking.CanBeCancelledBy(tt.actor))
		})
	}
}

func TestIsUUID(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{"11111111-1111-1111-1111-111111111111", true},
		{"AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE", true},
		{"", false},
		{"11111111-1111-1111-1111-11111111111", false},
		{"11111111_1111-1111-1111-111111111111", false},
		{"g1111111-1111-1111-1111-111111111111", false},
		{"11111111-1111-1111-1111-11111111111g", false},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			assert.Equal(t, tt.want, IsUUID(tt.value))
		})
	}
}

func TestValidationError(t *testing.T) {
	tests := []struct {
		name   string
		fields map[string]string
		want   string
	}{
		{"no fields", nil, "validation failed"},
		{"one field", map[string]string{"title": "must not be empty"}, "validation failed: title must not be empty"},
		{"sorted fields", map[string]string{"title": "is required", "capacity": "must be positive"}, "validation failed: capacity must be positive; title is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, (&ValidationError{Fields: tt.fields}).Error())
		})
	}
}

func TestRefreshTokenReuseError(t *testing.T) {
	principal := Principal{UserID: "user", ClubID: "club", Role: RoleMember}
	err := &RefreshTokenReuseError{Principal: principal, FamilyID: "family"}

	require.ErrorIs(t, err, ErrUnauthorized)
	assert.Equal(t, "refresh token reuse detected", err.Error())
	assert.Equal(t, principal, err.Principal)
	assert.Equal(t, "family", err.FamilyID)
	assert.NotContains(t, err.Error(), "raw")
	assert.True(t, errors.Is(err, ErrUnauthorized))
}
