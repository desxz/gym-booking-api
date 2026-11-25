package domain

import (
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MinPasswordBytes = 8
	MaxPasswordBytes = 72
	MaxClassTitleLen = 120
	MinClassCapacity = 1
	MaxClassCapacity = 200
)

// Role is a closed set of authorization roles.
type Role string

const (
	RoleMember  Role = "member"
	RoleTrainer Role = "trainer"
)

func (r Role) Valid() bool {
	return r == RoleMember || r == RoleTrainer
}

// Principal is the authenticated identity extracted from a verified token.
type Principal struct {
	UserID string
	ClubID string
	Role   Role
}

// User is the persisted account. PasswordHash must never be serialized or logged.
type User struct {
	ID           string
	ClubID       string
	Email        string
	PasswordHash string
	Role         Role
	CreatedAt    time.Time
}

// LogValue implements slog.LogValuer and deliberately omits PasswordHash.
func (u User) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("id", u.ID),
		slog.String("club_id", u.ClubID),
		slog.String("role", string(u.Role)),
		slog.String("email", maskEmail(u.Email)),
	)
}

// maskEmail keeps the first character and the domain: m***@club-a.test.
func maskEmail(email string) string {
	at := strings.LastIndexByte(email, '@')
	if at <= 0 {
		return "***"
	}
	return email[:1] + "***" + email[at:]
}

// Class is a scheduled session owned by one club and taught by one trainer.
type Class struct {
	ID          string
	ClubID      string
	TrainerID   string
	Title       string
	StartsAt    time.Time
	Capacity    int
	BookedCount int
	CreatedAt   time.Time
}

// NewClass validates domain invariants and returns a fully formed class.
func NewClass(clubID, trainerID, title string, startsAt time.Time, capacity int, now time.Time) (Class, error) {
	title = strings.TrimSpace(title)
	fields := make(map[string]string)

	switch {
	case title == "":
		fields["title"] = "must not be empty"
	case utf8.RuneCountInString(title) > MaxClassTitleLen:
		fields["title"] = fmt.Sprintf("must be at most %d characters", MaxClassTitleLen)
	}
	if capacity < MinClassCapacity || capacity > MaxClassCapacity {
		fields["capacity"] = fmt.Sprintf("must be between %d and %d", MinClassCapacity, MaxClassCapacity)
	}
	if !startsAt.After(now) {
		fields["starts_at"] = "must be in the future"
	}
	if len(fields) > 0 {
		return Class{}, &ValidationError{Fields: fields}
	}

	return Class{
		ClubID:    clubID,
		TrainerID: trainerID,
		Title:     title,
		StartsAt:  startsAt,
		Capacity:  capacity,
	}, nil
}

// CanAcceptBooking is the single domain rule for whether a seat is available.
func (c Class) CanAcceptBooking(now time.Time) error {
	if !c.StartsAt.After(now) {
		return ErrClassAlreadyStarted
	}
	if c.BookedCount >= c.Capacity {
		return ErrClassFull
	}
	return nil
}

// Booking links one member to one class.
type Booking struct {
	ID        string
	ClubID    string
	ClassID   string
	UserID    string
	CreatedAt time.Time
}

// CanBeCancelledBy allows the owning member or any trainer in the same club.
func (b Booking) CanBeCancelledBy(actor Principal) bool {
	if b.ClubID != actor.ClubID {
		return false
	}
	return b.UserID == actor.UserID || actor.Role == RoleTrainer
}

// IsUUID reports whether s has the canonical 8-4-4-4-12 hex shape. It does not
// check version or variant because the database generates valid UUIDs.
func IsUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		switch i {
		case 8, 13, 18, 23:
			if s[i] != '-' {
				return false
			}
		default:
			if !isHex(s[i]) {
				return false
			}
		}
	}
	return true
}

func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// Session is the token pair returned by login and refresh.
type Session struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

// RefreshToken is the persisted, hashed half of a rotating refresh token.
// TokenHash is never logged or serialized.
type RefreshToken struct {
	ID         string
	FamilyID   string
	UserID     string
	ClubID     string
	TokenHash  string
	ExpiresAt  time.Time
	RevokedAt  *time.Time
	ReplacedBy *string
	CreatedAt  time.Time
}
