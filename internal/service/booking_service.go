package service

import (
	"context"
	"time"

	"github.com/muratgun/gym-booking-api/internal/domain"
)

//go:generate go run go.uber.org/mock/mockgen -source=booking_service.go -destination=mock_booking_deps_test.go -package=service

// BookingRepository is the persistence contract needed by BookingService.
type BookingRepository interface {
	CreateClass(ctx context.Context, class domain.Class) (domain.Class, error)
	ListClasses(ctx context.Context, clubID string) ([]domain.Class, error)
	CreateBooking(ctx context.Context, clubID, classID, userID, idempotencyKey string, now time.Time) (domain.Booking, error)
	CancelBooking(ctx context.Context, bookingID string, actor domain.Principal) error
}

type BookingService struct {
	repo BookingRepository
	now  func() time.Time
}

func NewBookingService(repo BookingRepository, clock func() time.Time) *BookingService {
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	return &BookingService{repo: repo, now: clock}
}

func (s *BookingService) CreateClass(
	ctx context.Context, actor domain.Principal, title string, startsAt time.Time, capacity int,
) (domain.Class, error) {
	class, err := domain.NewClass(actor.ClubID, actor.UserID, title, startsAt, capacity, s.now())
	if err != nil {
		return domain.Class{}, err
	}
	return s.repo.CreateClass(ctx, class)
}

func (s *BookingService) ListClasses(ctx context.Context, actor domain.Principal) ([]domain.Class, error) {
	return s.repo.ListClasses(ctx, actor.ClubID)
}

func (s *BookingService) BookClass(
	ctx context.Context, actor domain.Principal, classID, idempotencyKey string,
) (domain.Booking, error) {
	if err := requireUUID("class_id", classID); err != nil {
		return domain.Booking{}, err
	}
	return s.repo.CreateBooking(ctx, actor.ClubID, classID, actor.UserID, idempotencyKey, s.now())
}

func (s *BookingService) CancelBooking(ctx context.Context, actor domain.Principal, bookingID string) error {
	if err := requireUUID("booking_id", bookingID); err != nil {
		return err
	}
	return s.repo.CancelBooking(ctx, bookingID, actor)
}

func requireUUID(field, value string) error {
	if domain.IsUUID(value) {
		return nil
	}
	return &domain.ValidationError{Fields: map[string]string{field: "must be a valid UUID"}}
}
