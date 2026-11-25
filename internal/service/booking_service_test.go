package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/muratgun/gym-booking-api/internal/domain"
)

var (
	testNow       = time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	testClassID   = "33333333-3333-3333-3333-333333333301"
	testBookingID = "44444444-4444-4444-4444-444444444401"
	testMember    = domain.Principal{
		UserID: "11111111-1111-1111-1111-111111111102",
		ClubID: "11111111-1111-1111-1111-111111111111",
		Role:   domain.RoleMember,
	}
	testTrainer = domain.Principal{
		UserID: "11111111-1111-1111-1111-111111111101",
		ClubID: testMember.ClubID,
		Role:   domain.RoleTrainer,
	}
)

func TestBookingServiceDefaultClock(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := NewMockBookingRepository(ctrl)
	repo.EXPECT().CreateBooking(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _, _, _, _ string, now time.Time) (domain.Booking, error) {
			assert.False(t, now.IsZero())
			assert.Equal(t, time.UTC, now.Location())
			return domain.Booking{}, nil
		}).Times(1)

	_, err := NewBookingService(repo, nil).BookClass(context.Background(), testMember, testClassID, "")
	require.NoError(t, err)
}

func TestCreateClass(t *testing.T) {
	t.Run("persists a validated class for the actor's club", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := NewMockBookingRepository(ctrl)
		startsAt := testNow.Add(48 * time.Hour)
		repo.EXPECT().CreateClass(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, class domain.Class) (domain.Class, error) {
				assert.Equal(t, testTrainer.ClubID, class.ClubID)
				assert.Equal(t, testTrainer.UserID, class.TrainerID)
				assert.Equal(t, "Morning Strength", class.Title)
				assert.Equal(t, startsAt, class.StartsAt)
				assert.Equal(t, 12, class.Capacity)
				class.ID = testClassID
				return class, nil
			}).Times(1)

		class, err := NewBookingService(repo, func() time.Time { return testNow }).CreateClass(
			context.Background(), testTrainer, "  Morning Strength  ", startsAt, 12)
		require.NoError(t, err)
		assert.Equal(t, testClassID, class.ID)
	})

	t.Run("propagates repository errors", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := NewMockBookingRepository(ctrl)
		repoErr := errors.New("insert failed")
		repo.EXPECT().CreateClass(gomock.Any(), gomock.Any()).
			Return(domain.Class{}, repoErr).Times(1)

		_, err := NewBookingService(repo, func() time.Time { return testNow }).CreateClass(
			context.Background(), testTrainer, "Spin", testNow.Add(time.Hour), 10)
		require.ErrorIs(t, err, repoErr)
	})

	t.Run("rejects invalid input without touching the repository", func(t *testing.T) {
		tests := []struct {
			name      string
			title     string
			startsAt  time.Time
			capacity  int
			wantField string
		}{
			{"empty title", "", testNow.Add(time.Hour), 10, "title"},
			{"title too long", strings.Repeat("a", domain.MaxClassTitleLen+1), testNow.Add(time.Hour), 10, "title"},
			{"zero capacity", "Spin", testNow.Add(time.Hour), 0, "capacity"},
			{"capacity above maximum", "Spin", testNow.Add(time.Hour), domain.MaxClassCapacity + 1, "capacity"},
			{"start in the past", "Spin", testNow.Add(-time.Minute), 10, "starts_at"},
			{"start exactly now", "Spin", testNow, 10, "starts_at"},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				ctrl := gomock.NewController(t)
				repo := NewMockBookingRepository(ctrl)
				_, err := NewBookingService(repo, func() time.Time { return testNow }).CreateClass(
					context.Background(), testTrainer, tt.title, tt.startsAt, tt.capacity)

				var validation *domain.ValidationError
				require.ErrorAs(t, err, &validation)
				assert.Contains(t, validation.Fields, tt.wantField)
			})
		}
	})
}

func TestBookClass(t *testing.T) {
	t.Run("books for the authenticated member and forwards the idempotency key", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := NewMockBookingRepository(ctrl)
		repo.EXPECT().CreateBooking(
			gomock.Any(), testMember.ClubID, testClassID, testMember.UserID, "idem-1", testNow,
		).Return(domain.Booking{
			ID: testBookingID, ClubID: testMember.ClubID, ClassID: testClassID, UserID: testMember.UserID,
		}, nil).Times(1)

		booking, err := NewBookingService(repo, func() time.Time { return testNow }).
			BookClass(context.Background(), testMember, testClassID, "idem-1")
		require.NoError(t, err)
		assert.Equal(t, testBookingID, booking.ID)
	})

	t.Run("propagates domain errors", func(t *testing.T) {
		tests := []struct {
			name string
			err  error
		}{
			{"class full", domain.ErrClassFull},
			{"already booked", domain.ErrAlreadyBooked},
			{"class already started", domain.ErrClassAlreadyStarted},
			{"idempotency conflict", domain.ErrIdempotencyConflict},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				ctrl := gomock.NewController(t)
				repo := NewMockBookingRepository(ctrl)
				repo.EXPECT().CreateBooking(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
					Return(domain.Booking{}, tt.err).Times(1)

				_, err := NewBookingService(repo, func() time.Time { return testNow }).
					BookClass(context.Background(), testMember, testClassID, "")
				require.ErrorIs(t, err, tt.err)
			})
		}
	})

	t.Run("rejects malformed UUIDs without touching the repository", func(t *testing.T) {
		for _, classID := range []string{"", "not-a-uuid", "33333333-3333-3333-3333-33333333330"} {
			t.Run(classID, func(t *testing.T) {
				ctrl := gomock.NewController(t)
				repo := NewMockBookingRepository(ctrl)
				_, err := NewBookingService(repo, func() time.Time { return testNow }).
					BookClass(context.Background(), testMember, classID, "")

				var validation *domain.ValidationError
				require.ErrorAs(t, err, &validation)
				assert.Contains(t, validation.Fields, "class_id")
			})
		}
	})
}

func TestCancelBooking(t *testing.T) {
	t.Run("cancels with the actor's scope", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := NewMockBookingRepository(ctrl)
		repo.EXPECT().CancelBooking(gomock.Any(), testBookingID, gomock.Eq(testMember)).Return(nil).Times(1)

		require.NoError(t, NewBookingService(repo, func() time.Time { return testNow }).
			CancelBooking(context.Background(), testMember, testBookingID))
	})

	t.Run("propagates authorization and tenant errors", func(t *testing.T) {
		tests := []struct {
			name string
			err  error
		}{
			{"not the owner", domain.ErrForbidden},
			{"other tenant looks like not found", domain.ErrNotFound},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				ctrl := gomock.NewController(t)
				repo := NewMockBookingRepository(ctrl)
				repo.EXPECT().CancelBooking(gomock.Any(), testBookingID, gomock.Eq(testMember)).
					Return(tt.err).Times(1)

				err := NewBookingService(repo, func() time.Time { return testNow }).
					CancelBooking(context.Background(), testMember, testBookingID)
				require.ErrorIs(t, err, tt.err)
			})
		}
	})

	t.Run("rejects malformed booking IDs", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := NewMockBookingRepository(ctrl)
		err := NewBookingService(repo, func() time.Time { return testNow }).
			CancelBooking(context.Background(), testMember, "123")

		var validation *domain.ValidationError
		require.ErrorAs(t, err, &validation)
		assert.Contains(t, validation.Fields, "booking_id")
	})
}

func TestListClasses(t *testing.T) {
	t.Run("returns classes for the actor's club", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := NewMockBookingRepository(ctrl)
		repo.EXPECT().ListClasses(gomock.Any(), testMember.ClubID).
			Return([]domain.Class{{ID: testClassID, ClubID: testMember.ClubID}}, nil).Times(1)

		classes, err := NewBookingService(repo, func() time.Time { return testNow }).
			ListClasses(context.Background(), testMember)
		require.NoError(t, err)
		require.Len(t, classes, 1)
		assert.Equal(t, testClassID, classes[0].ID)
	})

	t.Run("propagates repository errors", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := NewMockBookingRepository(ctrl)
		repoErr := errors.New("query failed")
		repo.EXPECT().ListClasses(gomock.Any(), testMember.ClubID).Return(nil, repoErr).Times(1)

		_, err := NewBookingService(repo, func() time.Time { return testNow }).
			ListClasses(context.Background(), testMember)
		require.ErrorIs(t, err, repoErr)
	})
}
